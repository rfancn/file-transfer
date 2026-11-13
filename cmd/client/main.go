package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/mattn/go-runewidth"

	"github.com/rfancn/file-transfer/internal/auth"
	"github.com/rfancn/file-transfer/internal/client"
	"github.com/rfancn/file-transfer/internal/cliui"
	"github.com/rfancn/file-transfer/internal/config"
	"github.com/rfancn/file-transfer/internal/protocol"
	"github.com/rfancn/file-transfer/internal/version"
)

const (
	exitOK        = 0
	exitGeneric   = 1
	exitUsage     = 2
	exitAuth      = 3
	exitNetwork   = 4
	exitServer    = 5
	exitLocalFile = 6
	exitPartial   = 7
	exitInterrupt = 130
)

type globals struct {
	configPath    string
	server        string
	debug         bool
	username      string
	passwordStdin bool
}

func main() {
	g := globals{}
	fs := flag.NewFlagSet("ft-client", flag.ExitOnError)
	fs.StringVar(&g.configPath, "config", "", "客户端配置")
	fs.StringVar(&g.configPath, "c", "", "客户端配置（简写）")
	fs.StringVar(&g.server, "server", "", "覆盖服务器地址")
	fs.BoolVar(&g.debug, "debug", false, "输出调试信息")
	fs.StringVar(&g.username, "username", "", "账号")
	fs.BoolVar(&g.passwordStdin, "password-stdin", false, "从标准输入读取密码")
	showVersion := fs.Bool("version", false, "输出版本")
	_ = fs.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println(version.Version)
		return
	}
	cfg, err := config.LoadClient(g.configPath)
	if err != nil {
		fail(exitUsage, "加载配置失败: %v", err)
	}
	if g.server != "" {
		cfg.Server.URL = g.server
	}
	if cfg.Server.URL == "" {
		cfgPath := g.configPath
		if cfgPath == "" {
			cfgPath = os.Getenv("FT_CLIENT_CONFIG")
		}
		if cfgPath == "" {
			cfgPath = config.DefaultClientConfig
		}
		fail(exitUsage, "未配置 server.url：请在 %s 的 [server] 段设置，或使用 --server / FT_SERVER_URL", cfgPath)
	}
	args := fs.Args()
	if len(args) == 0 {
		runMenu(g, cfg)
		return
	}
	switch args[0] {
	case "register":
		runRegister(g, cfg, args[1:])
	case "list":
		runList(g, cfg, args[1:])
	case "download":
		runDownload(g, cfg, args[1:])
	case "upload":
		runUpload(g, cfg, args[1:])
	default:
		fail(exitUsage, "未知子命令: %s", args[0])
	}
}

func runRegister(g globals, cfg config.ClientConfig, args []string) {
	fs := flag.NewFlagSet("register", flag.ExitOnError)
	name := fs.String("username", g.username, "要注册的用户名")
	flagArgs, _ := splitArgs(args, map[string]bool{"h": true, "help": true})
	_ = fs.Parse(flagArgs)
	fmt.Println("提示：注册用户名请使用本人姓名的全拼（例如 zhangsan）。")
	if *name == "" {
		value, err := cliui.Input("用户名（姓名全拼）", currentUser(), validateLocalUsername)
		if canceledByUser(err) {
			fail(exitInterrupt, "已取消")
		}
		if err != nil {
			fail(exitUsage, "读取用户名失败: %v", err)
		}
		*name = value
	}
	if err := validateLocalUsername(*name); err != nil {
		fail(exitUsage, "%v", err)
	}
	password, ok := promptNewPassword()
	if !ok {
		return
	}
	cl, err := client.New(cfg)
	if err != nil {
		fail(exitNetwork, "%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := cl.Register(ctx, *name, password)
	if err != nil {
		failFor(err)
	}
	fmt.Printf("注册成功: %s\n", resp.Username)
	saveClientCredentials(resp.Username, password)
	printCredentialsSaved()
}

func promptNewPassword() (string, bool) {
	for {
		pass, err := cliui.PasswordWithValidate("密码 (至少8位, 含大小写/数字/特殊字符)", func(s string) error {
			return auth.ValidatePassword(s)
		})
		if canceledByUser(err) {
			return "", false
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "读取密码失败: %v\n", err)
			return "", false
		}
		confirm, err := cliui.Password("确认密码")
		if canceledByUser(err) {
			return "", false
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "读取确认密码失败: %v\n", err)
			return "", false
		}
		if pass != confirm {
			fmt.Fprintln(os.Stderr, "两次输入的密码不一致，请重新输入")
			continue
		}
		return pass, true
	}
}

func runList(g globals, cfg config.ClientConfig, args []string) {
	if len(args) != 1 || (args[0] != "inbound" && args[0] != "upload" && args[0] != "outbound") {
		fail(exitUsage, "用法: ft-client list <inbound|outbound|upload>")
	}
	cl := login(g, cfg)
	if cl == nil {
		return
	}
	files, err := cl.List(context.Background(), args[0])
	if err != nil {
		failFor(err)
	}
	printFiles(args[0], files)
}

func runDownload(g globals, cfg config.ClientConfig, args []string) {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	out := fs.String("output", ".", "下载目录")
	force := fs.Bool("force", false, "覆盖已存在文件")
	flagArgs, rest := splitArgs(args, map[string]bool{"force": true, "h": true, "help": true})
	_ = fs.Parse(flagArgs)
	if len(rest) != 2 || (rest[0] != "inbound" && rest[0] != "upload") {
		fail(exitUsage, "用法: ft-client download <inbound|upload> <相对路径> [--output DIR] [--force]")
	}
	cl := login(g, cfg)
	if cl == nil {
		return
	}
	if err := cl.Download(context.Background(), rest[0], rest[1], *out, *force, progressPrinter()); err != nil {
		failFor(err)
	}
	fmt.Printf("下载完成: %s\n", *out)
}

func runUpload(g globals, cfg config.ClientConfig, args []string) {
	fs := flag.NewFlagSet("upload", flag.ExitOnError)
	dest := fs.String("dest", "", "目标名称")
	yes := fs.Bool("yes", false, "跳过确认")
	flagArgs, rest := splitArgs(args, map[string]bool{"yes": true, "h": true, "help": true})
	_ = fs.Parse(flagArgs)
	if len(rest) != 1 {
		fail(exitUsage, "用法: ft-client upload <本地路径> [--dest NAME] [--yes]")
	}
	if _, err := os.Stat(rest[0]); err != nil {
		fail(exitLocalFile, "%v", err)
	}
	cl := login(g, cfg)
	if cl == nil {
		return
	}
	completed, err := uploadFlow(cl, cfg, rest[0], *dest, *yes)
	if err != nil {
		if errors.Is(err, errPartialUpload) {
			fail(exitPartial, "%v", err)
		}
		failFor(err)
	}
	if completed {
		os.Exit(exitOK)
	}
}

func printUploadSummary(cl *client.Client, local, batch string, plan client.UploadPlan) {
	target := plan.Files[0].Remote
	if plan.IsDir {
		parts := strings.SplitN(plan.Files[0].Remote, "/", 4)
		if len(parts) >= 3 {
			target = strings.Join(parts[:3], "/") + "/"
		}
	}

	fmt.Println("========================================")
	fmt.Println("  上传拷出确认")
	fmt.Println("========================================")
	fmt.Printf("  账号    : %s\n", cl.Username())
	fmt.Printf("  批次号  : %s\n", batch)
	fmt.Printf("  源路径  : %s\n", local)
	fmt.Printf("  目标    : %s\n", target)
	fmt.Printf("  文件数  : %d\n", len(plan.Files))
	fmt.Printf("  总大小  : %s\n", formatSize(plan.Total))
	fmt.Println("----------------------------------------")
	const maxList = 10
	for i, f := range plan.Files {
		if i >= maxList {
			fmt.Printf("  ... 还有 %d 个文件\n", len(plan.Files)-maxList)
			break
		}
		name := path.Base(f.Remote)
		if plan.IsDir {
			name = strings.TrimPrefix(f.Remote, target)
		}
		fmt.Printf("  %-40s %s\n", name, formatSize(f.Size))
	}
	fmt.Println("========================================")
}

var errPartialUpload = errors.New("部分文件上传失败")

// uploadFlow 处理批次选择、中断检测、摘要展示和上传。
func uploadFlow(cl *client.Client, cfg config.ClientConfig, source, dest string, skipConfirm bool) (completed bool, err error) {
	if _, err := os.Stat(source); err != nil {
		return false, err
	}
	absSource, err := filepath.Abs(source)
	if err != nil {
		return false, err
	}
	ctx := context.Background()
	batch, force, confirmed, cleanup, err := chooseBatch(ctx, cl, absSource, dest)
	if err != nil {
		return false, err
	}
	if batch == "" {
		return false, nil
	}
	plan, err := client.PlanUploadTo(source, dest, "outbound/"+batch)
	if err != nil {
		return false, err
	}
	if len(plan.Files) == 0 {
		fmt.Println("没有可上传的文件")
		return false, nil
	}
	printUploadSummary(cl, source, batch, plan)
	printUploadWarning(plan, cfg)
	if !confirmed && !skipConfirm {
		ok, err := cliui.Confirm("确认上传", false)
		if canceledByUser(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}
	if cleanup != nil {
		cleanup()
	}

	task := &client.UploadTask{
		Source: absSource,
		Dest:   dest,
		Batch:  "outbound/" + batch,
		Files:  make([]client.TaskFile, 0, len(plan.Files)),
	}
	for _, f := range plan.Files {
		local := f.Local
		if abs, absErr := filepath.Abs(local); absErr == nil {
			local = abs
		}
		task.Files = append(task.Files, client.TaskFile{Local: local, Remote: f.Remote, Size: f.Size})
	}
	if err := client.SaveUploadTask(cl.Username(), task); err != nil {
		fmt.Fprintf(os.Stderr, "警告: 保存上传任务失败: %v\n", err)
	}

	res := cl.Upload(ctx, plan, progressPrinter(), force)
	if len(res.Failed) == 0 {
		_ = client.CompleteUploadTask(cl.Username(), absSource, dest)
	}
	printUploadResult(res, batch)
	if len(res.Failed) > 0 {
		return true, errPartialUpload
	}
	return true, nil
}

// chooseBatch 检测未完成任务并决定批次号。
// 返回 batch 为空表示用户取消；confirmed 表示用户已在中断提示中确认上传；
// cleanup 在用户最终确认后执行，用于丢弃旧批次的分片状态。
func chooseBatch(ctx context.Context, cl *client.Client, source, dest string) (batch string, force, confirmed bool, cleanup func(), err error) {
	pending, listErr := client.ListPendingUploadTasks(cl.Username())
	if listErr != nil {
		fmt.Fprintf(os.Stderr, "警告: 读取上传任务失败: %v\n", listErr)
	}

	var match *client.UploadTask
	var others []client.UploadTask
	for i := range pending {
		if pending[i].Source == source && pending[i].Dest == dest {
			match = &pending[i]
		} else {
			others = append(others, pending[i])
		}
	}

	if match != nil {
		progress, inspectErr := cl.InspectTask(ctx, *match)
		if inspectErr != nil {
			return "", false, false, nil, inspectErr
		}
		if progress.DoneFiles == len(progress.Task.Files) {
			// 远端已完整，旧任务标记完成，按新上传处理。
			_ = client.CompleteUploadTask(cl.Username(), source, dest)
		} else {
			// 仅用于比较本次文件集，前缀不影响结果。
			current, planErr := client.PlanUpload(source, dest)
			if planErr != nil {
				return "", false, false, nil, planErr
			}
			changes := client.DiffTaskFiles(progress.Task.Files, current.Files, progress.Uploaded, source)
			printTaskDiff(progress, current, changes)
			choice, err := cliui.Select("如何处理上次中断的上传", []string{
				"断点续传（保留远端旧文件）",
				"重新上传（新批次，与源路径一致）",
				"取消",
			})
			if canceledByUser(err) {
				return "", false, false, nil, nil
			}
			if err != nil {
				return "", false, false, nil, err
			}
			switch choice {
			case 0:
				return strings.TrimPrefix(progress.Task.Batch, "outbound/"), false, true, nil, nil
			case 1:
				old := progress.Pending
				cleanup = func() {
					discardCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					cl.DiscardInterrupted(discardCtx, old)
					cancel()
					_ = client.CompleteUploadTask(cl.Username(), source, dest)
				}
			default:
				return "", false, false, nil, nil
			}
		}
	}

	for _, t := range others {
		fmt.Fprintf(os.Stderr, "提示: 存在未完成的上传批次 %s（源路径 %s），如不再需要可在 Console 清理。\n",
			strings.TrimPrefix(t.Batch, "outbound/"), t.Source)
	}

	batch, err = cl.NextBatch(ctx)
	if err != nil {
		return "", false, false, nil, err
	}
	return batch, true, false, nil, nil
}

func printTaskDiff(p client.TaskProgress, current client.UploadPlan, changes []client.FileChange) {
	batch := strings.TrimPrefix(p.Task.Batch, "outbound/")
	fmt.Println("========================================")
	fmt.Println("  上次中断的上传")
	fmt.Println("========================================")
	fmt.Printf("  批次号    : %s\n", batch)
	fmt.Printf("  源路径    : %s\n", p.Task.Source)
	fmt.Printf("  上次任务  : %d 个文件, %s（已上传 %d 个, %s）\n",
		len(p.Task.Files), formatSize(p.TotalBytes), p.DoneFiles, formatSize(p.UploadedBytes))
	fmt.Printf("  本次计划  : %d 个文件, %s\n", len(current.Files), formatSize(current.Total))
	fmt.Println("----------------------------------------")

	added, missing, modified, same := splitChanges(changes)
	if len(added)+len(missing)+len(modified) == 0 {
		fmt.Printf("  文件集未变化: %d 个文件\n", len(same))
	} else {
		fmt.Println("  文件变化:")
		printChangeGroup("+", "新增", added)
		printChangeGroup("-", "缺失", missing)
		printChangeGroup("~", "修改", modified)
		if len(same) > 0 {
			uploaded := 0
			for _, c := range same {
				if c.Uploaded {
					uploaded++
				}
			}
			fmt.Printf("    = 未变 %d 个（已上传 %d, 待上传 %d）\n", len(same), uploaded, len(same)-uploaded)
		}
	}
	fmt.Println("----------------------------------------")
	if len(missing) > 0 {
		fmt.Println("  断点续传不会删除远端已上传的旧文件。")
		fmt.Println("  如需最新批次与源路径完全一致, 请选择「重新上传」。")
	} else {
		fmt.Println("  断点续传会沿用批次号, 只补齐未完成或修改过的文件。")
	}
	fmt.Println("========================================")
}

func splitChanges(changes []client.FileChange) (added, missing, modified, same []client.FileChange) {
	for _, c := range changes {
		switch c.Kind {
		case client.ChangeAdded:
			added = append(added, c)
		case client.ChangeMissing:
			missing = append(missing, c)
		case client.ChangeModified:
			modified = append(modified, c)
		default:
			same = append(same, c)
		}
	}
	return
}

func printChangeGroup(mark, label string, items []client.FileChange) {
	if len(items) == 0 {
		return
	}
	fmt.Printf("    %s %s %d 个:\n", mark, label, len(items))
	const maxList = 10
	for i, c := range items {
		if i >= maxList {
			fmt.Printf("        ... 还有 %d 个\n", len(items)-maxList)
			break
		}
		fmt.Printf("        %-40s %s\n", c.Name, changeDetail(c))
	}
}

func changeDetail(c client.FileChange) string {
	switch c.Kind {
	case client.ChangeAdded:
		return formatSize(c.NewSize)
	case client.ChangeMissing:
		detail := formatSize(c.OldSize)
		if c.Uploaded {
			detail += "  [已上传, 续传不会删除]"
		}
		return detail
	case client.ChangeModified:
		detail := formatSize(c.OldSize) + " -> " + formatSize(c.NewSize)
		if c.Uploaded {
			detail += "  [远端已有旧版本]"
		}
		return detail
	default:
		detail := formatSize(c.NewSize)
		if c.Uploaded {
			detail += "  [已上传]"
		} else {
			detail += "  [待上传]"
		}
		return detail
	}
}

// printUploadWarning 在文件数或总大小超过配置阈值时提醒用户确认，避免误传。
func printUploadWarning(plan client.UploadPlan, cfg config.ClientConfig) {
	warnSize, _ := config.ParseSize(cfg.Upload.WarnSize)
	var reasons []string
	if cfg.Upload.WarnFiles > 0 && len(plan.Files) > cfg.Upload.WarnFiles {
		reasons = append(reasons, fmt.Sprintf("文件数超过 %d 个", cfg.Upload.WarnFiles))
	}
	if warnSize > 0 && plan.Total > warnSize {
		reasons = append(reasons, fmt.Sprintf("总大小超过 %s", formatSize(warnSize)))
	}
	if len(reasons) == 0 {
		return
	}
	fmt.Println("========================================")
	fmt.Println("  注意: 本次上传数据量较大")
	fmt.Println("========================================")
	fmt.Printf("  文件数  : %d\n", len(plan.Files))
	fmt.Printf("  总大小  : %s\n", formatSize(plan.Total))
	fmt.Printf("  原因    : %s\n", strings.Join(reasons, "，"))
	fmt.Println("  请确认源路径和文件清单是否正确，避免误传。")
	fmt.Println("========================================")
}

func printUploadResult(res client.UploadResult, batch string) {
	fmt.Printf("上传完成: 成功 %d，跳过 %d，失败 %d，共 %s\n", res.Uploaded, res.Skipped, len(res.Failed), formatSize(res.Bytes))
	for _, f := range res.Failed {
		fmt.Fprintf(os.Stderr, "失败: %s\n", f)
	}
	fmt.Println()
	fmt.Println("========================================")
	if len(res.Failed) == 0 {
		fmt.Println("  请发起拷出文件流程")
		fmt.Println("========================================")
		fmt.Printf("  批次号  : %s\n", batch)
		fmt.Println("----------------------------------------")
		fmt.Printf("  请在拷出文件流程中务必提供批次号 %s。\n", batch)
	} else {
		fmt.Println("  上传未完成，暂不能发起拷出")
		fmt.Println("========================================")
		fmt.Printf("  批次号  : %s\n", batch)
		fmt.Println("----------------------------------------")
		fmt.Println("  请重新运行 ft-client 选择断点续传，完成后再发起拷出流程。")
	}
	fmt.Println("========================================")
}

func runMenu(g globals, cfg config.ClientConfig) {
	var cl *client.Client
	if !hasSavedCredentials() {
		cl = firstRun(g, cfg)
		if cl == nil {
			return
		}
	}
	for {
		if cl != nil {
			fmt.Printf("\n当前用户: %s\n", cl.Username())
		} else {
			fmt.Println()
		}
		choice, err := cliui.Select("请选择操作", []string{
			"上传拷出文件（上传到 outbound）",
			"下载拷入文件（从 inbound 下载）",
			"下载上传文件（从 upload 下载）",
			"退出",
		})
		if errors.Is(err, cliui.ErrCanceled) {
			// ESC：交互式终端下留在菜单；管道 EOF 时退出。
			if cliui.IsInteractive() {
				continue
			}
			return
		}
		if errors.Is(err, cliui.ErrInterrupt) {
			return
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			continue
		}
		switch choice {
		case 0:
			cl = ensureLogin(cl, g, cfg)
			if cl == nil {
				continue
			}
			menuUpload(cl, cfg)
		case 1:
			cl = ensureLogin(cl, g, cfg)
			if cl == nil {
				continue
			}
			menuDownload(cl, cfg, "inbound")
		case 2:
			cl = ensureLogin(cl, g, cfg)
			if cl == nil {
				continue
			}
			menuDownload(cl, cfg, "upload")
		case 3:
			return
		}
	}
}

func menuUpload(cl *client.Client, cfg config.ClientConfig) {
	localPath, err := cliui.PathInput("请输入上传文件路径", "")
	if canceledByUser(err) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return
	}
	if localPath == "" {
		return
	}
	completed, err := uploadFlow(cl, cfg, localPath, "", false)
	if err != nil {
		if errors.Is(err, errPartialUpload) {
			fail(exitPartial, "%v", err)
		}
		failFor(err)
	}
	if completed {
		os.Exit(exitOK)
	}
}

// ensureLogin 在第一次执行操作时才登录，避免启动时就要求输入用户名和密码。
func ensureLogin(cl *client.Client, g globals, cfg config.ClientConfig) *client.Client {
	if cl != nil {
		return cl
	}
	return login(g, cfg)
}

type downloadEntry struct {
	Name  string
	Path  string
	IsDir bool
	Size  int64
}

type downloadAction struct {
	Kind  string
	Entry downloadEntry
}

func browseEntries(files []protocol.FileInfo, cur string) []downloadEntry {
	prefix := ""
	if cur != "" {
		prefix = cur + "/"
	}
	seenDir := map[string]bool{}
	var entries []downloadEntry
	for _, f := range files {
		if prefix != "" && !strings.HasPrefix(f.Path, prefix) {
			continue
		}
		rest := strings.TrimPrefix(f.Path, prefix)
		if rest == "" {
			continue
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			name := rest[:i]
			if !seenDir[name] {
				seenDir[name] = true
				entries = append(entries, downloadEntry{Name: name, Path: prefix + name, IsDir: true})
			}
			continue
		}
		entries = append(entries, downloadEntry{Name: rest, Path: f.Path, Size: f.Size})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})
	return entries
}

func parentDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return ""
}

func menuDownload(cl *client.Client, cfg config.ClientConfig, dir string) {
	if dir == "upload" {
		menuMultiDownload(cl, cfg)
		return
	}
	ctx := context.Background()
	files, err := cl.List(ctx, dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return
	}
	cur := ""
	for {
		entries := browseEntries(files, cur)
		var items []string
		var actions []downloadAction
		var localFiles []downloadEntry
		for _, e := range entries {
			if e.IsDir {
				items = append(items, "[目录] "+e.Name+"/")
				actions = append(actions, downloadAction{Kind: "dir", Entry: e})
				continue
			}
			localFiles = append(localFiles, e)
		}
		if len(localFiles) > 0 {
			fmt.Println("  当前目录文件（不支持单独下载，请下载整个目录）:")
			for _, e := range localFiles {
				fmt.Printf("    %-40s %s\n", e.Name, formatSize(e.Size))
			}
		}
		if cur != "" {
			items = append(items, "[返回上级]")
			actions = append(actions, downloadAction{Kind: "up"})
			items = append(items, "[下载当前目录全部文件]")
			actions = append(actions, downloadAction{Kind: "download-dir"})
		}
		items = append(items, "[退出]")
		actions = append(actions, downloadAction{Kind: "exit"})

		where := dir + "/"
		if cur != "" {
			where = dir + "/" + cur + "/"
		}
		idx, err := cliui.Select("下载目录: "+where, items)
		if errors.Is(err, cliui.ErrInterrupt) {
			fail(exitInterrupt, "已取消")
		}
		if errors.Is(err, cliui.ErrCanceled) {
			if cur != "" {
				cur = parentDir(cur)
				continue
			}
			return
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			return
		}
		act := actions[idx]
		switch act.Kind {
		case "dir":
			choice, err := cliui.Select("选中目录 "+act.Entry.Name+"/", []string{
				"进入目录查看",
				"下载整个目录",
				"取消",
			})
			if canceledByUser(err) {
				continue
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "错误: %v\n", err)
				continue
			}
			switch choice {
			case 0:
				cur = act.Entry.Path
			case 1:
				downloadDir(cl, dir, act.Entry.Path, files, cfg.Download.NameWidth)
			}
		case "up":
			cur = parentDir(cur)
		case "download-dir":
			downloadDir(cl, dir, cur, files, cfg.Download.NameWidth)
		case "exit":
			return
		}
	}
}

// selectedByParent 报告路径是否已被某个上级目录选中。
func selectedByParent(path string, selected map[string]downloadEntry) bool {
	for _, sel := range selected {
		if sel.IsDir && strings.HasPrefix(path, sel.Path+"/") {
			return true
		}
	}
	return false
}

// fileExcluded 报告路径或其任一上级目录是否被单独排除。
func fileExcluded(path string, excluded map[string]bool) bool {
	for p := path; p != ""; p = parentDir(p) {
		if excluded[p] {
			return true
		}
	}
	return false
}

func fileSelected(path string, selected map[string]downloadEntry, excluded map[string]bool) bool {
	if fileExcluded(path, excluded) {
		return false
	}
	if _, ok := selected[path]; ok {
		return true
	}
	return selectedByParent(path, selected)
}

func clearExcluded(excluded map[string]bool, path string) {
	delete(excluded, path)
	prefix := path + "/"
	for p := range excluded {
		if strings.HasPrefix(p, prefix) {
			delete(excluded, p)
		}
	}
}

// dirSelectionState 返回目录下文件是否全选或部分选中。
func dirSelectionState(dir string, files []protocol.FileInfo, selected map[string]downloadEntry, excluded map[string]bool) (checked, partial bool) {
	prefix := dir + "/"
	total, selectedCount := 0, 0
	for _, f := range files {
		if !strings.HasPrefix(f.Path, prefix) {
			continue
		}
		total++
		if fileSelected(f.Path, selected, excluded) {
			selectedCount++
		}
	}
	if total == 0 {
		if _, ok := selected[dir]; ok {
			return true, false
		}
		return false, false
	}
	if selectedCount == 0 {
		return false, false
	}
	if selectedCount == total {
		return true, false
	}
	return false, true
}

func expandSelection(selected map[string]downloadEntry, files []protocol.FileInfo, excluded map[string]bool) []protocol.FileInfo {
	seen := map[string]bool{}
	var targets []protocol.FileInfo
	for _, sel := range selected {
		if sel.IsDir {
			prefix := sel.Path + "/"
			for _, f := range files {
				if strings.HasPrefix(f.Path, prefix) && !fileExcluded(f.Path, excluded) && !seen[f.Path] {
					seen[f.Path] = true
					targets = append(targets, f)
				}
			}
			continue
		}
		if fileExcluded(sel.Path, excluded) {
			continue
		}
		for _, f := range files {
			if f.Path == sel.Path && !seen[f.Path] {
				seen[f.Path] = true
				targets = append(targets, f)
			}
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].Path < targets[j].Path })
	return targets
}

type summaryItem struct {
	Name      string
	Size      int64
	IsDir     bool
	FileCount int
}

// middleTruncate 按显示宽度做中间省略，保留路径开头和文件名结尾。
func middleTruncate(s string, width int) string {
	if runewidth.StringWidth(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	headWidth := (width - 1) / 2
	tailWidth := width - 1 - headWidth
	rs := []rune(s)
	var head strings.Builder
	w := 0
	for _, r := range rs {
		rw := runewidth.RuneWidth(r)
		if w+rw > headWidth {
			break
		}
		head.WriteRune(r)
		w += rw
	}
	var tail []rune
	w = 0
	for i := len(rs) - 1; i >= 0; i-- {
		rw := runewidth.RuneWidth(rs[i])
		if w+rw > tailWidth {
			break
		}
		tail = append([]rune{rs[i]}, tail...)
		w += rw
	}
	return head.String() + "…" + string(tail)
}

func coveredBy(path string, covered map[string]bool) bool {
	for p := range covered {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// buildSummaryItems 把选中的目录折叠为 "目录/*"，部分选中的目录展开为具体文件。
func buildSummaryItems(selected map[string]downloadEntry, files []protocol.FileInfo, excluded map[string]bool) []summaryItem {
	covered := map[string]bool{}
	var items []summaryItem
	var dirs, fileEntries []downloadEntry
	for _, sel := range selected {
		if sel.IsDir {
			dirs = append(dirs, sel)
		} else {
			fileEntries = append(fileEntries, sel)
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i].Path) < len(dirs[j].Path) })
	for _, d := range dirs {
		if coveredBy(d.Path, covered) {
			continue
		}
		prefix := d.Path + "/"
		count, selectedCount := 0, 0
		var size int64
		for _, f := range files {
			if !strings.HasPrefix(f.Path, prefix) {
				continue
			}
			count++
			size += f.Size
			if fileSelected(f.Path, selected, excluded) {
				selectedCount++
			}
		}
		if count > 0 && selectedCount == count {
			items = append(items, summaryItem{Name: d.Path + "/", Size: size, IsDir: true, FileCount: count})
			covered[d.Path] = true
			continue
		}
		for _, f := range files {
			if strings.HasPrefix(f.Path, prefix) && fileSelected(f.Path, selected, excluded) && !coveredBy(f.Path, covered) {
				items = append(items, summaryItem{Name: f.Path, Size: f.Size})
				covered[f.Path] = true
			}
		}
	}
	for _, f := range fileEntries {
		if coveredBy(f.Path, covered) || fileExcluded(f.Path, excluded) {
			continue
		}
		for _, pf := range files {
			if pf.Path == f.Path {
				items = append(items, summaryItem{Name: f.Path, Size: pf.Size})
				break
			}
		}
		covered[f.Path] = true
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items
}

func printSelectionSummary(dir string, items []summaryItem, totalFiles int, total int64, nameWidth int) {
	fmt.Println("========================================")
	fmt.Println("  下载清单确认")
	fmt.Println("========================================")
	fmt.Printf("  共 %d 个文件，%s（路径相对 %s/）\n", totalFiles, formatSize(total), strings.TrimRight(dir, "/"))
	fmt.Println("----------------------------------------")
	const maxList = 10
	for i, item := range items {
		if i >= maxList {
			fmt.Printf("  ... 其余 %d 项（仍会下载，共 %d 个文件）\n", len(items)-maxList, totalFiles)
			break
		}
		name := padDisplay(middleTruncate(item.Name, nameWidth), nameWidth)
		if item.IsDir {
			fmt.Printf("  %s  整目录，%d 个文件，%s\n", name, item.FileCount, formatSize(item.Size))
			continue
		}
		fmt.Printf("  %s %s\n", name, formatSize(item.Size))
	}
	fmt.Println("========================================")
}

func downloadSelection(cl *client.Client, cfg config.ClientConfig, dir string, selected map[string]downloadEntry, files []protocol.FileInfo, excluded map[string]bool) {
	targets := expandSelection(selected, files, excluded)
	if len(targets) == 0 {
		fmt.Println("选中项中没有可下载的文件")
		return
	}
	var total int64
	for _, f := range targets {
		total += f.Size
	}
	printSelectionSummary(dir, buildSummaryItems(selected, files, excluded), len(targets), total, cfg.Download.NameWidth)
	ok, err := cliui.Confirm("确认下载清单", false)
	if canceledByUser(err) || (err == nil && !ok) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return
	}
	out, err := cliui.PathInput("保存路径", defaultDownloadDir())
	if canceledByUser(err) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return
	}
	if abs, absErr := filepath.Abs(out); absErr == nil {
		out = abs
	}
	fmt.Printf("保存路径: %s\n", out)
	var failed int
	for _, f := range targets {
		if err := cl.Download(context.Background(), dir, f.Path, out, false, progressPrinter()); err != nil {
			fmt.Fprintf(os.Stderr, "失败: %s: %v\n", f.Path, err)
			failed++
		}
	}
	fmt.Printf("下载完成: 成功 %d，失败 %d\n", len(targets)-failed, failed)
	if failed > 0 {
		fail(exitPartial, "部分文件下载失败")
	}
	os.Exit(exitOK)
}

func menuMultiDownload(cl *client.Client, cfg config.ClientConfig) {
	ctx := context.Background()
	files, err := cl.List(ctx, "upload")
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return
	}
	cur := ""
	selected := map[string]downloadEntry{}
	excluded := map[string]bool{}
	for {
		entries := browseEntries(files, cur)
		var items []cliui.MultiItem
		var actions []downloadAction
		for _, e := range entries {
			var checked, partial bool
			if e.IsDir {
				checked, partial = dirSelectionState(e.Path, files, selected, excluded)
			} else {
				checked = fileSelected(e.Path, selected, excluded)
			}
			if e.IsDir {
				items = append(items, cliui.MultiItem{
					Text: "[目录] " + e.Name + "/", Selectable: true, Checked: checked, Partial: partial,
				})
			} else {
				name := truncateDisplay(e.Name, cfg.Download.NameWidth)
				text := fmt.Sprintf("[文件] %s %s", padDisplay(name, cfg.Download.NameWidth), formatSize(e.Size))
				items = append(items, cliui.MultiItem{Text: text, Selectable: true, Checked: checked})
			}
			actions = append(actions, downloadAction{Kind: "entry", Entry: e})
		}
		items = append(items, cliui.MultiItem{Text: "[确认下载清单]"})
		actions = append(actions, downloadAction{Kind: "download"})
		if cur != "" {
			items = append(items, cliui.MultiItem{Text: "[返回上级]"})
			actions = append(actions, downloadAction{Kind: "up"})
		}
		items = append(items, cliui.MultiItem{Text: "[退出]"})
		actions = append(actions, downloadAction{Kind: "exit"})

		where := "upload/"
		if cur != "" {
			where = "upload/" + cur + "/"
		}
		idx, err := cliui.MultiSelect("下载上传文件: "+where, items)
		for i, act := range actions {
			if act.Kind != "entry" || !items[i].Touched {
				continue
			}
			e := act.Entry
			if items[i].Checked {
				clearExcluded(excluded, e.Path)
				if _, ok := selected[e.Path]; !ok && !selectedByParent(e.Path, selected) {
					selected[e.Path] = e
				}
				continue
			}
			if _, ok := selected[e.Path]; ok {
				delete(selected, e.Path)
			} else {
				excluded[e.Path] = true
			}
			if e.IsDir {
				prefix := e.Path + "/"
				for p := range selected {
					if strings.HasPrefix(p, prefix) {
						delete(selected, p)
					}
				}
			}
		}
		if errors.Is(err, cliui.ErrInterrupt) {
			fail(exitInterrupt, "已取消")
		}
		if errors.Is(err, cliui.ErrCanceled) {
			if cur != "" {
				cur = parentDir(cur)
				continue
			}
			return
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "错误: %v\n", err)
			return
		}
		act := actions[idx]
		switch act.Kind {
		case "entry":
			if act.Entry.IsDir {
				cur = act.Entry.Path
			}
		case "download":
			downloadSelection(cl, cfg, "upload", selected, files, excluded)
		case "up":
			cur = parentDir(cur)
		case "exit":
			return
		}
	}
}

func defaultDownloadDir() string {
	if wd, err := os.Getwd(); err == nil && wd != "" {
		return wd
	}
	return "."
}

func downloadDir(cl *client.Client, dir, cur string, files []protocol.FileInfo, nameWidth int) {
	prefix := ""
	if cur != "" {
		prefix = cur + "/"
	}
	var targets []protocol.FileInfo
	var total int64
	for _, f := range files {
		if prefix != "" && !strings.HasPrefix(f.Path, prefix) {
			continue
		}
		targets = append(targets, f)
		total += f.Size
	}
	if len(targets) == 0 {
		fmt.Println("当前目录没有可下载的文件")
		return
	}
	printDownloadSummary(dir, cur, targets, total, browseEntries(files, cur), nameWidth)
	ok, err := cliui.Confirm("确认下载", false)
	if canceledByUser(err) || (err == nil && !ok) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return
	}
	out, err := cliui.PathInput("保存路径", defaultDownloadDir())
	if canceledByUser(err) {
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return
	}
	if abs, absErr := filepath.Abs(out); absErr == nil {
		out = abs
	}
	fmt.Printf("保存路径: %s\n", out)
	var failed int
	for _, f := range targets {
		if err := cl.Download(context.Background(), dir, f.Path, out, false, progressPrinter()); err != nil {
			fmt.Fprintf(os.Stderr, "失败: %s: %v\n", f.Path, err)
			failed++
		}
	}
	fmt.Printf("下载完成: 成功 %d，失败 %d\n", len(targets)-failed, failed)
	if failed > 0 {
		fail(exitPartial, "部分文件下载失败")
	}
	os.Exit(exitOK)
}

// padDisplay 按终端显示宽度右填充空格，保证中文文件名后的大小列对齐。
func padDisplay(s string, width int) string {
	w := runewidth.StringWidth(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// truncateDisplay 按终端显示宽度截断字符串。
func truncateDisplay(s string, width int) string {
	if runewidth.StringWidth(s) <= width {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > width {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String()
}

func printDownloadSummary(dir, cur string, targets []protocol.FileInfo, total int64, entries []downloadEntry, nameWidth int) {
	where := dir + "/"
	if cur != "" {
		where = dir + "/" + cur + "/"
	}
	fmt.Println("========================================")
	fmt.Println("  下载确认")
	fmt.Println("========================================")
	fmt.Printf("  下载目录  : %s\n", where)
	fmt.Printf("  文件数    : %d\n", len(targets))
	fmt.Printf("  总大小    : %s\n", formatSize(total))
	fmt.Println("----------------------------------------")
	const maxList = 10
	for i, e := range entries {
		if i >= maxList {
			fmt.Printf("  ... 还有 %d 项\n", len(entries)-maxList)
			break
		}
		if e.IsDir {
			fmt.Printf("  [目录] %s/\n", e.Name)
		} else {
			name := truncateDisplay(e.Name, nameWidth)
			fmt.Printf("  [文件] %s %s\n", padDisplay(name, nameWidth), formatSize(e.Size))
		}
	}
	fmt.Println("========================================")
}

// firstRun 在首次使用（无本地凭证）时引导用户登录或注册。
func firstRun(g globals, cfg config.ClientConfig) *client.Client {
	choice, err := cliui.Select("首次使用，请选择", []string{
		"登录已有账号",
		"注册新用户",
		"退出",
	})
	if errors.Is(err, cliui.ErrInterrupt) {
		os.Exit(exitInterrupt)
	}
	if err != nil {
		return nil
	}
	switch choice {
	case 0:
		return login(g, cfg)
	case 1:
		return registerNew(g, cfg)
	default:
		return nil
	}
}

// registerNew 注册新用户并自动登录。
func registerNew(g globals, cfg config.ClientConfig) *client.Client {
	cl, err := client.New(cfg)
	if err != nil {
		fail(exitNetwork, "%v", err)
	}
	name := g.username
	if name == "" {
		fmt.Println("注册用户名请使用本人姓名的全拼（例如 zhangsan）。")
		name, err = cliui.Input("用户名（姓名全拼）", currentUser(), validateLocalUsername)
		if canceledByUser(err) {
			return nil
		}
		if err != nil {
			fail(exitUsage, "读取用户名失败: %v", err)
		}
	}
	if err := validateLocalUsername(name); err != nil {
		fail(exitUsage, "%v", err)
	}
	password, ok := promptNewPassword()
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	resp, err := cl.Register(ctx, name, password)
	cancel()
	if err != nil {
		failFor(err)
	}
	fmt.Printf("注册成功: %s\n", resp.Username)
	saveClientCredentials(resp.Username, password)
	printCredentialsSaved()
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	err = cl.Login(ctx, resp.Username, password)
	cancel()
	if err != nil {
		failFor(err)
	}
	return cl
}

// hasSavedCredentials 报告是否存在与当前服务器匹配的本地凭证。
func hasSavedCredentials() bool {
	_, err := config.LoadClientCredentials()
	return err == nil
}

func login(g globals, cfg config.ClientConfig) *client.Client {
	cl, err := client.New(cfg)
	if err != nil {
		fail(exitNetwork, "%v", err)
	}

	saved, savedErr := config.LoadClientCredentials()
	savedUsable := savedErr == nil
	if savedErr != nil && !errors.Is(savedErr, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "读取保存的账号失败: %v\n", savedErr)
	}
	if savedUsable && g.username == "" && !g.passwordStdin {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		loginErr := cl.Login(ctx, saved.Username, saved.Password)
		cancel()
		if loginErr == nil {
			return cl
		}
		if isInvalidCredentials(loginErr) {
			fmt.Fprintln(os.Stderr, "保存的账号已失效，请重新登录")
			_ = config.RemoveClientCredentials()
		} else {
			failFor(loginErr)
		}
	}

	username := g.username
	if username == "" {
		defaultName := currentUser()
		if savedUsable {
			defaultName = saved.Username
		}
		username, err = cliui.Input("账号", defaultName, nil)
		if canceledByUser(err) {
			return nil
		}
		if err != nil {
			fail(exitUsage, "读取账号失败: %v", err)
		}
	}
	password := ""
	if g.passwordStdin {
		password, err = cliui.ReadLine()
		if err != nil {
			fail(exitUsage, "读取密码失败: %v", err)
		}
	}
	attempts := 1
	if !g.passwordStdin {
		attempts = 3
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if password == "" {
			password, err = cliui.Password("密码")
			if canceledByUser(err) {
				return nil
			}
			if err != nil {
				fail(exitUsage, "读取密码失败: %v", err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		lastErr = cl.Login(ctx, username, password)
		cancel()
		if lastErr == nil {
			// --password-stdin 属于非交互场景，避免把密码意外写入磁盘。
			if !g.passwordStdin {
				saveClientCredentials(username, password)
			}
			return cl
		}
		if isInvalidCredentials(lastErr) {
			fmt.Fprintln(os.Stderr, "认证失败：用户名或密码错误")
			password = ""
			continue
		}
		if ae, ok := client.AsAPIError(lastErr); ok && ae.Code == "not_registered" {
			if c := registerInteractive(cl, username); c != nil {
				return c
			}
			return nil
		}
		break
	}
	failFor(lastErr)
	return nil
}

// registerInteractive 在用户未注册时引导注册，成功后自动登录。
func registerInteractive(cl *client.Client, username string) *client.Client {
	fmt.Printf("用户 %s 未注册。\n", username)
	fmt.Println("注册用户名请使用本人姓名的全拼（例如 zhangsan）。")
	name, err := cliui.Input("注册用户名（姓名全拼）", username, validateLocalUsername)
	if canceledByUser(err) {
		return nil
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取注册用户名失败: %v\n", err)
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = username
	}
	ok, err := cliui.Confirm(fmt.Sprintf("使用用户名 %s 注册", name), true)
	if canceledByUser(err) || (err == nil && !ok) {
		return nil
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取确认失败: %v\n", err)
		return nil
	}
	password, ok := promptNewPassword()
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	resp, err := cl.Register(ctx, name, password)
	cancel()
	if err != nil {
		failFor(err)
	}
	fmt.Printf("注册成功: %s\n", resp.Username)
	saveClientCredentials(resp.Username, password)
	printCredentialsSaved()
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	err = cl.Login(ctx, resp.Username, password)
	cancel()
	if err != nil {
		failFor(err)
	}
	return cl
}

func isInvalidCredentials(err error) bool {
	ae, ok := client.AsAPIError(err)
	return ok && ae.Code == "invalid_credentials"
}

func printCredentialsSaved() {
	if path, err := config.ClientCredentialsPath(); err == nil {
		fmt.Printf("凭证已保存到 %s\n", path)
	}
}

func saveClientCredentials(username, password string) {
	err := config.SaveClientCredentials(config.ClientCredentials{
		Username: username,
		Password: password,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "警告: 保存账号信息失败: %v\n", err)
	}
}

func splitArgs(args []string, boolFlags map[string]bool) ([]string, []string) {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		if j := strings.IndexByte(name, '='); j >= 0 {
			flags = append(flags, a)
			continue
		}
		flags = append(flags, a)
		if boolFlags[name] {
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			i++
			flags = append(flags, args[i])
		}
	}
	return flags, pos
}

func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	return "unknown"
}

func validateLocalUsername(name string) error {
	if name == "" {
		return errors.New("用户名不能为空")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return errors.New("用户名只能包含字母、数字、下划线和连字符")
		}
	}
	return nil
}

func printFiles(dir string, files []protocol.FileInfo) {
	fmt.Printf("目录: %s/\n", dir)
	if len(files) == 0 {
		fmt.Println("无文件")
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	width := 4
	for _, f := range files {
		if n := len([]rune(sanitize(f.Path))); n > width {
			width = n
		}
	}
	if width > 60 {
		width = 60
	}
	fmt.Printf("%-*s  %-10s  %s\n", width, "路径", "大小", "修改时间")
	var total int64
	for _, f := range files {
		total += f.Size
		p := sanitize(f.Path)
		if len([]rune(p)) > width {
			p = string([]rune(p)[:width-1]) + "…"
		}
		fmt.Printf("%-*s  %-10s  %s\n", width, p, formatSize(f.Size), f.LastModified.Local().Format("2006-01-02 15:04:05"))
	}
	fmt.Printf("\n共 %d 个文件，%s\n", len(files), formatSize(total))
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			b.WriteString(fmt.Sprintf("\\x%02x", r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func formatSize(n int64) string {
	const (
		KiB = 1024
		MiB = 1024 * KiB
		GiB = 1024 * MiB
		TiB = 1024 * GiB
	)
	switch {
	case n >= TiB:
		return fmt.Sprintf("%.1f TiB", float64(n)/TiB)
	case n >= GiB:
		return fmt.Sprintf("%.1f GiB", float64(n)/GiB)
	case n >= MiB:
		return fmt.Sprintf("%.1f MiB", float64(n)/MiB)
	case n >= KiB:
		return fmt.Sprintf("%.1f KiB", float64(n)/KiB)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func progressPrinter() client.ProgressFunc {
	var last time.Time
	lastDone := int64(-1)
	return func(done, total int64) {
		if total <= 0 || done == lastDone {
			return
		}
		if time.Since(last) < 200*time.Millisecond && done < total {
			return
		}
		last = time.Now()
		lastDone = done
		pct := float64(done) / float64(total) * 100
		fmt.Fprintf(os.Stderr, "\r%5.1f%%  %s / %s", pct, formatSize(done), formatSize(total))
		if done >= total {
			fmt.Fprintln(os.Stderr)
		}
	}
}

// canceledByUser 在 Ctrl-C 时退出程序，ESC 时返回 true 交由调用方返回上级。
func canceledByUser(err error) bool {
	if errors.Is(err, cliui.ErrInterrupt) {
		fail(exitInterrupt, "已取消")
	}
	return errors.Is(err, cliui.ErrCanceled)
}

func fail(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(code)
}

func failFor(err error) {
	if err == nil {
		return
	}
	if ae, ok := client.AsAPIError(err); ok {
		switch {
		case ae.Code == "not_registered":
			fail(exitAuth, "用户未注册，请先运行 ft-client register")
		case ae.Status == 401:
			fail(exitAuth, "认证失败: %s", ae.Message)
		case ae.Status >= 500:
			fail(exitServer, "服务端错误: %s", ae.Message)
		case ae.Status >= 400:
			fail(exitGeneric, "%s", ae.Message)
		}
	}
	low := strings.ToLower(err.Error())
	if strings.Contains(low, "connection") || strings.Contains(low, "dial") || strings.Contains(low, "no such host") {
		fail(exitNetwork, "网络错误: %v", err)
	}
	if errors.Is(err, io.EOF) {
		fail(exitGeneric, "连接中断")
	}
	if _, ok := err.(*url.Error); ok {
		fail(exitNetwork, "网络错误: %v", err)
	}
	fail(exitGeneric, "%v", err)
}
