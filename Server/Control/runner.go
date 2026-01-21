package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type controlConfig struct {
	workDir string
	goBin   string

	imgDir      string
	aName       string
	bName       string
	imageName   string
	srcPort     int
	dstPort     int
	criuHost    string
	verbose     bool
	noCleanup   bool
	clientLog   string
	criuInB     string
	bInitPID    int
	aInitPID    int
	restoredPID int

	// Incremental pre-copy (CRIU pre-dump) settings.
	//
	// predumpRounds：
	//   - final dump 之前执行多少轮 CRIU pre-dump。
	//   - 每一轮都会在进程继续运行（--leave-running）的情况下复制脏页，
	//     这能显著降低大内存服务的 final dump 体积/耗时。
	//
	// predumpLastDir：
	//   - 最后一轮成功 pre-dump 的目录名（位于 imgDir 下）。
	//   - 若非空，final dump 会通过 --prev-images-dir 走增量 dump。
	predumpRounds  int
	predumpLastDir string

	// scheme2: out-of-band commit notify address (client listens on UDP).
	commitAddr string
}

func mountIfExists(args []string, hostPath, containerPath, mode string) []string {
	if hostPath == "" || containerPath == "" {
		return args
	}
	if mode == "" {
		mode = "ro"
	}
	if fi, err := os.Stat(hostPath); err == nil && fi != nil {
		args = append(args, "-v", fmt.Sprintf("%s:%s:%s", hostPath, containerPath, mode))
	}
	return args
}

// 这是 server 控制层（Control Layer）在 PoC/MVP 阶段的“单机编排器”。
// 它复刻 container_pro 的 injectctl run：
// - build server/client
// - podman 起 A(源) + B(壳)
// - 起 client
// - SIGTERM 触发 migrate
// - CRIU dump -> kill A
// - nsenter 到 B restore
// - SIGUSR2 让服务 rebind
// - 汇总：客户端感知服务中断时间

func parseCommonFlags(cmd string, args []string) *controlConfig {
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)

	cfg := &controlConfig{}
	fs.StringVar(&cfg.imgDir, "img-dir", "/dev/shm/criu-inject", "共享镜像目录")
	fs.StringVar(&cfg.aName, "a-name", "inj-src", "A(源)容器名")
	fs.StringVar(&cfg.bName, "b-name", "inj-dst", "B(壳)容器名")
	fs.StringVar(&cfg.imageName, "image", "wrapper-pingserver-criu", "server 镜像名")
	fs.IntVar(&cfg.srcPort, "src-port", 5242, "A 对外暴露的 host UDP 端口")
	fs.IntVar(&cfg.dstPort, "dst-port", 5243, "B 对外暴露的 host UDP 端口")
	fs.StringVar(&cfg.commitAddr, "commit-addr", "127.0.0.1:7360", "方案2：client 侧 commit 通道监听地址(udp)，B restore+rebind 后由 Control 发送 commit")
	criuHostBin := ""
	fs.StringVar(&criuHostBin, "criu-host-bin", "", "host 上 criu 可执行文件路径")
	fs.BoolVar(&cfg.verbose, "verbose", false, "打印更多执行细节")
	fs.BoolVar(&cfg.noCleanup, "no-cleanup", false, "失败时不清理容器")
	fs.IntVar(&cfg.predumpRounds, "predump-rounds", 2, "迁移前执行 pre-dump 轮数（0=关闭；建议>=1用于大内存）")
	_ = fs.Parse(args)

	wd, err := os.Getwd()
	if err != nil {
		dief("getwd failed: %v", err)
	}
	cfg.workDir = wd
	cfg.goBin = mustPickGoBin()
	cfg.clientLog = filepath.Join(wd, "client.log")

	criuHost, err := pickCRIUHostBin(criuHostBin)
	if err != nil {
		dief("missing dependency: criu (host): %v", err)
	}
	cfg.criuHost = criuHost
	cfg.criuInB = filepath.Join("/hostbin", filepath.Base(criuHost))

	return cfg
}

type commitMsg struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

func sendCommit(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	c, err := net.Dial("udp", addr)
	if err != nil {
		return err
	}
	defer c.Close()

	msg := commitMsg{Type: "commit", ID: fmt.Sprintf("commit-%d", time.Now().UnixNano())}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = c.Write(append(b, '\n'))
	return err
}

func buildSkipMntArgs(imgDir string) []string {
	skipMnts := []string{imgDir, "/proc", "/sys", "/sys/fs/cgroup", "/dev", "/dev/shm", "/dev/pts", "/dev/mqueue", "/run", "/etc/hosts", "/etc/resolv.conf", "/etc/hostname", "/run/.containerenv", "/run/secrets"}
	skipArgs := []string{}
	for _, m := range skipMnts {
		skipArgs = append(skipArgs, "--skip-mnt", m)
	}
	return skipArgs
}

func cleanContainers(aName, bName string) {
	_ = exec.Command("sudo", "podman", "rm", "-f", aName).Run()
	_ = exec.Command("sudo", "podman", "rm", "-f", bName).Run()
}

func buildAndImage(cfg *controlConfig) {
	step("构建：编译 + 镜像", func() error {
		if err := goBuildStatic(cfg.verbose, cfg.workDir, cfg.goBin, filepath.Join(cfg.workDir, "Server", "server_bin"), "./Server/APP"); err != nil {
			return err
		}
		if err := goBuildStatic(cfg.verbose, cfg.workDir, cfg.goBin, filepath.Join(cfg.workDir, "Client", "client_bin"), "./Client/APP"); err != nil {
			return err
		}
		return runQuiet("sudo", "podman", "build", "-t", cfg.imageName, filepath.Join(cfg.workDir, "Server"))
	})
}

func prepareImgDir(imgDir string) {
	step("准备：镜像目录", func() error {
		if err := runQuiet("sudo", "rm", "-rf", imgDir); err != nil {
			return err
		}
		return runQuiet("sudo", "mkdir", "-p", imgDir)
	})
}

func startA(cfg *controlConfig) {
	step("启动：A(源)", func() error {
		_ = runQuiet("sudo", "podman", "rm", "-f", cfg.aName)
		args := []string{
			"podman", "run", "-d", "--privileged", "--name", cfg.aName, "--pid=host",
			"-p", fmt.Sprintf("%d:4242/udp", cfg.srcPort),
			"-v", fmt.Sprintf("%s:%s:rw", cfg.imgDir, cfg.imgDir),
			"-e", "MIGRATE_ADDR=127.0.0.1",
			"-e", fmt.Sprintf("MIGRATE_PORT=%d", cfg.dstPort),
			"-e", "QUIET=1",
			cfg.imageName,
		}
		if err := runQuiet("sudo", args...); err != nil {
			return err
		}
		pid, err := podmanStatePID(cfg.aName)
		if err != nil {
			return err
		}
		cfg.aInitPID = pid
		return nil
	})
}

func startB(cfg *controlConfig) {
	step("启动：B(壳)", func() error {
		_ = runQuiet("sudo", "podman", "rm", "-f", cfg.bName)

		args := []string{
			"podman", "run", "-d", "--privileged", "--name", cfg.bName, "--pid=host",
			"-p", fmt.Sprintf("%d:4242/udp", cfg.dstPort),
			"-v", fmt.Sprintf("%s:%s:rw", cfg.imgDir, cfg.imgDir),
			"--entrypoint", "sleep",
		}

		// 将 host 上 criu 的所在目录挂进容器，避免假设 /usr/local/sbin。
		hostBinDir := filepath.Dir(cfg.criuHost)
		args = mountIfExists(args, hostBinDir, "/hostbin", "ro")

		// criu/loader 依赖的动态库：不同发行版路径不同，按存在性选择性挂载。
		args = mountIfExists(args, "/lib64", "/lib64", "ro")
		args = mountIfExists(args, "/usr/lib64", "/usr/lib64", "ro")
		args = mountIfExists(args, "/lib/x86_64-linux-gnu", "/lib/x86_64-linux-gnu", "ro")
		args = mountIfExists(args, "/usr/lib/x86_64-linux-gnu", "/usr/lib/x86_64-linux-gnu", "ro")

		args = append(args, cfg.imageName, "infinity")

		if err := runQuiet("sudo", args...); err != nil {
			return err
		}
		pid, err := podmanStatePID(cfg.bName)
		if err != nil {
			return err
		}
		cfg.bInitPID = pid
		return nil
	})
}

func doMigrate(cfg *controlConfig, clientObs *clientObserver) {
	skipArgs := buildSkipMntArgs(cfg.imgDir)

	step("预拷贝：pre-dump(A)", func() error {
		if cfg.predumpRounds <= 0 {
			cfg.predumpLastDir = ""
			return nil
		}

		// A 的 PID 可能变化，实时从 podman 拿。
		pid, err := podmanStatePID(cfg.aName)
		if err != nil {
			return err
		}
		cfg.aInitPID = pid

		cfg.predumpLastDir = ""
		for i := 0; i < cfg.predumpRounds; i++ {
			dirName := fmt.Sprintf("pd-%d", i)
			imgSubdir := filepath.Join(cfg.imgDir, dirName)
			_ = runQuiet("sudo", "rm", "-rf", imgSubdir)
			if err := runQuiet("sudo", "mkdir", "-p", imgSubdir); err != nil {
				return err
			}

			// 这里使用的 CRIU pre-dump 关键参数：
			//   - --leave-running：不停止进程（即“预拷贝”阶段）。
			//   - --track-mem：启用脏页跟踪，为增量/多轮 pre-dump 做基础。
			//   - --prev-images-dir（从第 2 轮开始）：引用上一轮镜像目录，形成增量链。
			//   - --empty-ns net + --manage-cgroups=ignore：容器 PoC 的务实配置。
			args := []string{cfg.criuHost, "pre-dump", "-t", strconv.Itoa(cfg.aInitPID), "-D", imgSubdir, "-W", cfg.imgDir,
				"--shell-job", "--leave-running", "--empty-ns", "net", "--manage-cgroups=ignore", "--track-mem",
			}
			if i > 0 {
				// NOTE: --prev-images-dir is relative to -D. Our image dirs are siblings under cfg.imgDir.
				args = append(args, "--prev-images-dir", fmt.Sprintf("../pd-%d", i-1))
			}
			args = append(args, append(skipArgs, "-o", fmt.Sprintf("pre-dump-%d.log", i), "-v4")...)

			if err := runQuiet("sudo", args...); err != nil {
				// Fall back to normal (non-incremental) final dump.
				fmt.Fprintf(os.Stderr, "[控制端] 警告：pre-dump #%d 失败，将退化为普通 dump：%v\n", i, err)
				cfg.predumpLastDir = ""
				return nil
			}
			cfg.predumpLastDir = dirName
		}
		return nil
	})

	step("触发：迁移信号", func() error {
		// A 的 PID 可能变化，实时从 podman 拿。
		pid, err := podmanStatePID(cfg.aName)
		if err != nil {
			return err
		}
		cfg.aInitPID = pid
		_ = sudoKill(cfg.aInitPID, syscall.SIGTERM)
		if clientObs != nil {
			wait := 5 * time.Second
			// If we already did pre-dump, keep the gap to the final dump small to reduce newly dirtied pages.
			if cfg.predumpLastDir != "" {
				wait = 200 * time.Millisecond
			}
			select {
			case <-clientObs.migrateSeen:
			case <-time.After(wait):
				fmt.Fprintln(os.Stderr, "[控制端] 警告：未看到 migrate")
			}
		}
		return nil
	})

	step("检查点：dump(A)", func() error {
		args := []string{cfg.criuHost, "dump", "-t", strconv.Itoa(cfg.aInitPID), "-D", cfg.imgDir, "-W", cfg.imgDir,
			"--shell-job", "--empty-ns", "net", "--manage-cgroups=ignore",
		}
		if cfg.predumpLastDir != "" {
			// --prev-images-dir is relative to -D (cfg.imgDir).
			args = append(args, "--prev-images-dir", cfg.predumpLastDir)
			args = append(args, "--track-mem")
		}
		args = append(args, append(skipArgs, "-o", "dump.log", "-v4")...)
		return runQuiet("sudo", args...)
	})

	step("停止：A(快速)", func() error {
		_ = sudoKill(cfg.aInitPID, syscall.SIGKILL)
		return nil
	})

	step("恢复：注入到B", func() error {
		// B 的 PID 可能变化，实时从 podman 拿。
		pid, err := podmanStatePID(cfg.bName)
		if err != nil {
			return err
		}
		cfg.bInitPID = pid

		pidFile := filepath.Join(cfg.imgDir, "restored.pid")
		restoreLog := filepath.Join(cfg.imgDir, "restore.log")

		restoreArgs := []string{
			"restore", "-D", cfg.imgDir, "-W", cfg.imgDir,
			"--shell-job", "--restore-detached", "--mntns-compat-mode",
			"--root", "/", "--manage-cgroups=ignore",
			"--pidfile", pidFile,
			"-J", fmt.Sprintf("net:/proc/%d/ns/net", cfg.bInitPID),
			"-o", filepath.Base(restoreLog), "-v4",
		}

		nsenterArgs := []string{"nsenter", "-t", strconv.Itoa(cfg.bInitPID), "-m", "-n", "--", cfg.criuInB}
		nsenterArgs = append(nsenterArgs, restoreArgs...)

		cmd := exec.Command("sudo", nsenterArgs...)
		cmd.Dir = cfg.imgDir
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		start := time.Now()
		if err := cmd.Run(); err != nil {
			reportExecFailure(start, stdout.Bytes(), stderr.Bytes(), err)
			return err
		}

		rpid, err := readPIDFile(pidFile)
		if err != nil {
			return err
		}
		if err := sudoKill0(rpid); err != nil {
			return fmt.Errorf("restored pid not alive: pid=%d err=%w", rpid, err)
		}
		cfg.restoredPID = rpid
		if err := sudoKill(cfg.restoredPID, syscall.SIGUSR2); err != nil {
			return err
		}

		// 方案2：显式 commit 信号。
		// 目的：让 client 在 B 已 ready 后立刻 cutover，避免依赖业务 IO deadline 超时触发。
		// 注意：该信号是“加速路径”，发送失败不应中断迁移。
		time.Sleep(10 * time.Millisecond)
		if err := sendCommit(cfg.commitAddr); err != nil {
			fmt.Fprintf(os.Stderr, "[控制端] 警告：发送 commit 失败 addr=%s err=%v\n", cfg.commitAddr, err)
		}
		return nil
	})

	step("等待：客户端重连", func() error {
		if clientObs == nil {
			return nil
		}
		select {
		case <-clientObs.firstEchoAfterReconnect:
		case <-time.After(25 * time.Second):
		}
		return nil
	})
}

func runCmd(args []string) {
	cfg := parseCommonFlags("run", args)

	var clientProc *exec.Cmd
	var clientObs *clientObserver

	clean := func() { cleanContainers(cfg.aName, cfg.bName) }
	if !cfg.noCleanup {
		defer clean()
	}

	defer func() {
		if r := recover(); r != nil {
			if clientProc != nil && clientProc.Process != nil {
				_ = clientProc.Process.Signal(os.Interrupt)
			}
			if !cfg.noCleanup {
				clean()
			}
			fmt.Fprintf(os.Stderr, "%v\n", r)
			os.Exit(2)
		}
	}()

	buildAndImage(cfg)
	prepareImgDir(cfg.imgDir)
	startA(cfg)
	startB(cfg)

	step("启动：客户端", func() error {
		_ = os.Remove(cfg.clientLog)
		clientProc = exec.Command(filepath.Join(cfg.workDir, "Client", "client_bin"))
		clientProc.Env = append(os.Environ(), fmt.Sprintf("TARGET_ADDR=127.0.0.1:%d", cfg.srcPort))

		obs, err := startClientObserver(clientProc, cfg.clientLog)
		if err != nil {
			return err
		}
		clientObs = obs

		select {
		case <-clientObs.connected:
			return nil
		case <-time.After(8 * time.Second):
			fmt.Fprintln(os.Stderr, "[控制端] 警告：客户端连接超时")
			return nil
		}
	})

	doMigrate(cfg, clientObs)

	if clientObs != nil {
		dt := clientObs.downtime()
		if dt >= 0 {
			fmt.Printf("[客户端] 汇总：服务中断 %dms\n", dt.Milliseconds())
		}
	}

	if clientProc != nil && clientProc.Process != nil {
		_ = clientProc.Process.Signal(os.Interrupt)
	}
	if clientObs != nil {
		clientObs.stop()
	}
}

func upCmd(args []string) {
	cfg := parseCommonFlags("up", args)
	clean := func() { cleanContainers(cfg.aName, cfg.bName) }
	defer func() {
		if r := recover(); r != nil {
			if !cfg.noCleanup {
				clean()
			}
			fmt.Fprintf(os.Stderr, "%v\n", r)
			os.Exit(2)
		}
	}()

	buildAndImage(cfg)
	prepareImgDir(cfg.imgDir)
	startA(cfg)
	startB(cfg)

	fmt.Printf("[控制端] up 完成：A=%s(port=%d) B=%s(port=%d) imgDir=%s\n", cfg.aName, cfg.srcPort, cfg.bName, cfg.dstPort, cfg.imgDir)
}

func migrateCmd(args []string) {
	cfg := parseCommonFlags("migrate", args)

	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "%v\n", r)
			os.Exit(2)
		}
	}()

	// 这里只做迁移链路；client 由 run.sh 在前台跑。
	doMigrate(cfg, nil)
	fmt.Printf("[控制端] migrate 完成：restoredPID=%d\n", cfg.restoredPID)
}

func downCmd(args []string) {
	// down 只需要容器名与 imgDir，使用同一套解析函数获取默认值。
	cfg := parseCommonFlags("down", args)
	step("清理：容器", func() error {
		cleanContainers(cfg.aName, cfg.bName)
		return nil
	})
	step("清理：镜像目录", func() error {
		return runQuiet("sudo", "rm", "-rf", cfg.imgDir)
	})
}

func step(name string, fn func() error) {
	fmt.Printf("[控制端] 步骤：%s\n", name)
	if err := fn(); err != nil {
		panic(fmt.Errorf("步骤失败：%s：%w", name, err))
	}
}
