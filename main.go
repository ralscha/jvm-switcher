package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"jvm-switcher/internal/adoptium"
	"jvm-switcher/internal/build"
	"jvm-switcher/internal/catalog"
	"jvm-switcher/internal/config"
	"jvm-switcher/internal/jdk"
	"jvm-switcher/internal/project"
	"jvm-switcher/internal/store"
)

const usage = `jvm-switcher manages local JDK installations.

Usage:
  jvm-switcher <command> [arguments]

Commands:
  list, ls       List current JDK installations.
  install, i     Install an available remote JDK.
	exec           Run a command with a selected JDK.
  switch, s      Switch to the specified version or index number.
  remove, rm     Remove a specific version or index number.
	current        Show the active JDK.
	which          Show a command in the active JDK.
	doctor         Check the jvm-switcher setup.
	outdated       Show installed JDKs with newer patch releases.
	update         Install newer patch releases.
	prune          Remove superseded inactive patch releases.
  show           Show versions available for download.
  version        Show the jvm-switcher version.
  help, h        Show this help or help for one command.
`

var commandHelp = map[string]string{
	"list": `Usage: jvm-switcher list [--json]

List managed JDK installations. The displayed index can be used with switch or remove.
`,
	"install": `Usage: jvm-switcher install [version|index]

Download and install a JDK shown by 'jvm-switcher show'. A Java feature version,
distribution@feature, distribution@version, full version, or displayed index may be used.
Without an argument, use the nearest .jvm-switcher or .java-version file.
The first JDK installed is activated automatically.
`,
	"exec": `Usage: jvm-switcher exec [version|index] -- <command> [arguments]

Run a command with JAVA_HOME and PATH set for an installed JDK. Without a selector,
use the nearest .jvm-switcher or .java-version file. The global active JDK is unchanged.
`,
	"switch": `Usage: jvm-switcher switch <version|index>

Point the managed current path at an installed JDK.
`,
	"remove": `Usage: jvm-switcher remove <version|index>

Remove an installed JDK. The active JDK cannot be removed.
`,
	"current": `Usage: jvm-switcher current [--quiet|--json]

Show the active managed JDK and its installation path.
`,
	"which": `Usage: jvm-switcher which [command]

Show the path to a command in the active JDK. The default command is java.
`,
	"doctor": `Usage: jvm-switcher doctor

Check the active link, JAVA_HOME, PATH, and the active java executable.
`,
	"outdated": `Usage: jvm-switcher outdated [--json]

Show installed JDKs for which the configured source has a newer patch release.
`,
	"update": `Usage: jvm-switcher update [version|index|--all]

Install the latest patch for one JDK or all outdated JDKs. A project selector is
used when no argument is given. Existing patch releases are retained.
`,
	"prune": `Usage: jvm-switcher prune [--dry-run]

Remove inactive patch releases superseded by a newer installed release.
`,
	"show": `Usage: jvm-switcher show [--json] [--distribution <name>] [--feature <version>] [--lts] [--installed]

Show and filter JDKs available from the configured catalog or the default Adoptium source.
`,
	"version": `Usage: jvm-switcher version

Show the version this executable was built from.
`,
	"help": `Usage: jvm-switcher help [command]

Show the command overview, or the detailed help for one command.
`,
}

type remoteProvider interface {
	Available(context.Context) ([]jdk.Remote, error)
	Download(context.Context, jdk.Remote, io.Writer) error
}

type jdkStore interface {
	Root() string
	Lock(context.Context) (func() error, error)
	List() ([]jdk.Installed, error)
	Resolve(selector string) (jdk.Installed, error)
	InstallArchive(release jdk.Remote, archivePath string) error
	Switch(selector string) (jdk.Installed, error)
	Remove(selector string) (jdk.Installed, error)
}

type application struct {
	in        io.Reader
	out       io.Writer
	errOut    io.Writer
	provider  remoteProvider
	store     jdkStore
	source    string
	directory string
	environ   func() []string
	runChild  childRunner
}

type childRunner func(context.Context, string, []string, []string, string, io.Reader, io.Writer, io.Writer) error

func run(ctx context.Context, args []string) error {
	root, err := store.DefaultRoot()
	if err != nil {
		return err
	}
	configuration, err := config.Load(root)
	if err != nil {
		return err
	}
	httpClient := &http.Client{Timeout: 30 * time.Minute}
	var provider remoteProvider = adoptium.New(httpClient)
	source := "Adoptium API"
	if configuration.Catalog != "" {
		provider = catalog.New(httpClient, configuration.Catalog)
		source = configuration.Catalog
	}
	app := application{
		in:       os.Stdin,
		out:      os.Stdout,
		errOut:   os.Stderr,
		provider: provider,
		store:    store.New(root),
		source:   source,
		environ:  os.Environ,
		runChild: runChildProcess,
	}
	return app.execute(ctx, args)
}

func (app application) execute(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return app.outputf("%s", usage)
	}

	switch args[0] {
	case "help", "h", "--help", "-h":
		return app.help(args[1:])
	case "list", "ls":
		return app.list(args[1:])
	case "install", "i":
		return app.install(ctx, args[1:])
	case "exec":
		return app.execJDK(ctx, args[1:])
	case "switch", "s":
		return app.switchJDK(ctx, args[1:])
	case "remove", "rm":
		return app.remove(ctx, args[1:])
	case "current":
		return app.current(args[1:])
	case "which":
		return app.which(args[1:])
	case "doctor":
		return app.doctor(ctx, args[1:])
	case "outdated":
		return app.outdated(ctx, args[1:])
	case "update":
		return app.update(ctx, args[1:])
	case "prune":
		return app.prune(ctx, args[1:])
	case "show":
		return app.show(ctx, args[1:])
	case "version", "--version", "-v":
		return app.outputf("%s\n", build.Version())
	default:
		return fmt.Errorf("unknown command %q; run 'jvm-switcher help'", args[0])
	}
}

func (app application) help(args []string) error {
	if len(args) == 0 {
		return app.outputf("%s", usage)
	}
	if len(args) != 1 {
		return fmt.Errorf("usage: jvm-switcher help [command]")
	}
	command := canonicalCommand(args[0])
	help, ok := commandHelp[command]
	if !ok {
		return fmt.Errorf("unknown command %q", args[0])
	}
	return app.outputf("%s", help)
}

func (app application) list(args []string) error {
	jsonOutput := len(args) == 1 && args[0] == "--json"
	if len(args) != 0 && !jsonOutput {
		return fmt.Errorf("usage: jvm-switcher list [--json]")
	}
	installed, err := app.store.List()
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(app.out, installedRecords(installed))
	}
	if len(installed) == 0 {
		if err := app.outputf("No JDKs installed.\n"); err != nil {
			return err
		}
		return app.outputf("Install one with 'jvm-switcher install <version>'. Store: %s\n", app.store.Root())
	}
	if err := app.outputf("  #  DISTRIBUTION  VERSION                 STATUS\n"); err != nil {
		return err
	}
	for index, installation := range installed {
		status := ""
		if installation.Active {
			status = "active"
		}
		if err := app.outputf("%3d  %-12s  %-23s %s\n", index+1, displayDistribution(installation.Distribution), installation.Version, status); err != nil {
			return err
		}
	}
	return nil
}

func (app application) show(ctx context.Context, args []string) error {
	options, err := parseShowOptions(args)
	if err != nil {
		return fmt.Errorf("usage: jvm-switcher show [--json] [--distribution <name>] [--feature <version>] [--lts] [--installed]: %w", err)
	}
	releases, err := app.provider.Available(ctx)
	if err != nil {
		return err
	}
	installedIDs := make(map[string]bool)
	if options.installed {
		installed, err := app.store.List()
		if err != nil {
			return err
		}
		for _, installation := range installed {
			installedIDs[installation.ID()] = true
		}
	}
	filtered := filterReleases(releases, options, installedIDs)
	if options.json {
		return writeJSON(app.out, showDocument{Source: app.source, Releases: remoteRecords(filtered)})
	}
	if app.source != "" {
		if err := app.outputf("Source: %s\n", app.source); err != nil {
			return err
		}
	}
	if err := app.outputf("  #  DISTRIBUTION  JAVA  VERSION                 SIZE\n"); err != nil {
		return err
	}
	for _, item := range filtered {
		release := item.release
		if err := app.outputf("%3d  %-12s  %-4d  %-23s %s\n", item.index, displayDistribution(release.Distribution), release.Feature, release.Version, humanSize(release.Size)); err != nil {
			return err
		}
	}
	return nil
}

func (app application) install(ctx context.Context, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: jvm-switcher install [version|index]")
	}
	selector := ""
	if len(args) == 1 {
		selector = args[0]
	} else {
		var err error
		selector, _, err = project.Find(app.workingDirectory())
		if err != nil {
			return fmt.Errorf("find project JDK: %w", err)
		}
	}
	releases, err := app.provider.Available(ctx)
	if err != nil {
		return err
	}
	release, err := resolveRemote(releases, selector)
	if err != nil {
		return err
	}
	installed, err := app.store.List()
	if err != nil {
		return err
	}
	for _, installation := range installed {
		if installation.ID() == release.ID() {
			return fmt.Errorf("JDK %s is already installed", release.ID())
		}
	}
	activated, err := app.installRelease(ctx, release, func(installed []jdk.Installed) bool {
		return len(installed) == 0
	})
	if err != nil {
		return err
	}
	if activated {
		return app.outputf("Installed JDK %s and made it active.\n", release.ID())
	}
	return app.outputf("Installed JDK %s. Activate it with 'jvm-switcher switch %s'.\n", release.ID(), release.ID())
}

func (app application) installRelease(ctx context.Context, release jdk.Remote, shouldActivate func([]jdk.Installed) bool) (bool, error) {
	temporaryDirectory, err := os.MkdirTemp("", "jvm-switcher-*")
	if err != nil {
		return false, fmt.Errorf("create download directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporaryDirectory) }()
	archivePath := filepath.Join(temporaryDirectory, filepath.Base(release.FileName))
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		return false, fmt.Errorf("create download file: %w", err)
	}
	if err := app.outputf("Downloading %s (%s)...\n", release.ID(), humanSize(release.Size)); err != nil {
		return false, errors.Join(err, archiveFile.Close())
	}
	progress := &progressWriter{out: app.out, total: release.Size, interval: 500 * time.Millisecond}
	downloadErr := app.provider.Download(ctx, release, io.MultiWriter(archiveFile, progress))
	progressErr := progress.finish()
	closeErr := archiveFile.Close()
	if downloadErr != nil {
		return false, errors.Join(downloadErr, progressErr, closeErr)
	}
	if progressErr != nil {
		return false, progressErr
	}
	if closeErr != nil {
		return false, fmt.Errorf("save downloaded JDK: %w", closeErr)
	}
	activated := false
	err = app.withStoreLock(ctx, func() error {
		installed, err := app.store.List()
		if err != nil {
			return err
		}
		for _, installation := range installed {
			if installation.ID() == release.ID() {
				return fmt.Errorf("JDK %s was installed by another process", release.ID())
			}
		}
		if err := app.outputf("Installing...\n"); err != nil {
			return err
		}
		if err := app.store.InstallArchive(release, archivePath); err != nil {
			return err
		}
		if shouldActivate(installed) {
			if _, err := app.store.Switch(release.ID()); err != nil {
				return err
			}
			activated = true
		}
		return nil
	})
	return activated, err
}

func (app application) execJDK(ctx context.Context, args []string) error {
	separator := -1
	for index, argument := range args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator > 1 || separator == len(args)-1 {
		return fmt.Errorf("usage: jvm-switcher exec [version|index] -- <command> [arguments]")
	}
	selector := ""
	if separator == 1 {
		selector = args[0]
	} else {
		var err error
		selector, _, err = project.Find(app.workingDirectory())
		if err != nil {
			return fmt.Errorf("find project JDK: %w", err)
		}
	}
	installed, err := app.store.Resolve(selector)
	if err != nil {
		return err
	}
	environment := jdkEnvironment(app.environment(), installed.Path)
	command := resolveJDKExecutable(installed.Path, args[separator+1])
	runner := app.runChild
	if runner == nil {
		runner = runChildProcess
	}
	return runner(ctx, command, args[separator+2:], environment, app.workingDirectory(), app.in, app.out, app.errOut)
}

func (app application) workingDirectory() string {
	if app.directory != "" {
		return app.directory
	}
	directory, err := os.Getwd()
	if err != nil {
		return "."
	}
	return directory
}

func (app application) environment() []string {
	if app.environ != nil {
		return app.environ()
	}
	return os.Environ()
}

func jdkEnvironment(environment []string, javaHome string) []string {
	result := make([]string, 0, len(environment)+1)
	pathKey := "PATH"
	pathValue := ""
	for _, variable := range environment {
		name, value, ok := strings.Cut(variable, "=")
		if !ok {
			result = append(result, variable)
			continue
		}
		switch {
		case strings.EqualFold(name, "JAVA_HOME"):
			continue
		case strings.EqualFold(name, "PATH"):
			pathKey = name
			pathValue = value
			continue
		}
		result = append(result, variable)
	}
	result = append(result, "JAVA_HOME="+javaHome)
	pathValue = filepath.Join(javaHome, "bin") + string(os.PathListSeparator) + pathValue
	return append(result, pathKey+"="+pathValue)
}

func resolveJDKExecutable(javaHome, command string) string {
	if executable, ok := jdkCommandPath(javaHome, command); ok {
		return executable
	}
	return command
}

func jdkCommandPath(javaHome, command string) (string, bool) {
	if filepath.Base(command) != command {
		info, err := os.Stat(command)
		return command, err == nil && !info.IsDir()
	}
	candidate := filepath.Join(javaHome, "bin", command)
	candidates := []string{candidate}
	if runtime.GOOS == "windows" && filepath.Ext(candidate) == "" {
		candidates = append(candidates, candidate+".exe", candidate+".cmd", candidate+".bat")
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

func runChildProcess(ctx context.Context, executable string, args []string, environment []string, directory string, stdin io.Reader, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = environment
	command.Dir = directory
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func (app application) switchJDK(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: jvm-switcher switch <version|index>")
	}
	return app.withStoreLock(ctx, func() error {
		installed, err := app.store.Switch(args[0])
		if err != nil {
			return err
		}
		return app.outputf("Now using JDK %s.\n", installed.ID())
	})
}

func (app application) remove(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: jvm-switcher remove <version|index>")
	}
	return app.withStoreLock(ctx, func() error {
		removed, err := app.store.Remove(args[0])
		if err != nil {
			return err
		}
		return app.outputf("Removed JDK %s.\n", removed.ID())
	})
}

func (app application) withStoreLock(ctx context.Context, action func() error) (err error) {
	unlock, err := app.store.Lock(ctx)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, unlock())
	}()
	return action()
}

func (app application) current(args []string) error {
	quiet := len(args) == 1 && args[0] == "--quiet"
	jsonOutput := len(args) == 1 && args[0] == "--json"
	if len(args) != 0 && !quiet && !jsonOutput {
		return fmt.Errorf("usage: jvm-switcher current [--quiet|--json]")
	}
	active, index, err := app.activeJDKWithIndex()
	if err != nil {
		return err
	}
	if jsonOutput {
		record := installedRecord{Index: index, ID: active.ID(), Distribution: active.Distribution, Version: active.Version, Path: active.Path, Active: true}
		return writeJSON(app.out, record)
	}
	if quiet {
		return app.outputf("%s\n", active.ID())
	}
	return app.outputf("%s  %s\n", active.ID(), active.Path)
}

func (app application) which(args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("usage: jvm-switcher which [command]")
	}
	command := "java"
	if len(args) == 1 {
		command = args[0]
	}
	if _, err := app.activeJDK(); err != nil {
		return err
	}
	executable, ok := jdkCommandPath(filepath.Join(app.store.Root(), "current"), command)
	if !ok {
		return fmt.Errorf("command %q is not present in the active JDK", command)
	}
	return app.outputf("%s\n", executable)
}

func (app application) doctor(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: jvm-switcher doctor")
	}
	currentPath := filepath.Join(app.store.Root(), "current")
	currentBin := filepath.Join(currentPath, "bin")
	environment := app.environment()
	active, activeErr := app.activeJDK()
	checks := []doctorCheck{
		{name: "active JDK", ok: activeErr == nil, detail: errorDetail(activeErr, active.ID())},
		{name: "JAVA_HOME", ok: samePath(environmentValue(environment, "JAVA_HOME"), currentPath), detail: "expected " + currentPath},
		{name: "PATH", ok: pathContains(environmentValue(environment, "PATH"), currentBin), detail: "expected " + currentBin},
	}
	executable, executableOK := jdkCommandPath(currentPath, "java")
	checks = append(checks, doctorCheck{name: "java executable", ok: executableOK, detail: executable})
	if executableOK {
		runner := app.runChild
		if runner == nil {
			runner = runChildProcess
		}
		err := runner(ctx, executable, []string{"-version"}, environment, app.workingDirectory(), nil, io.Discard, io.Discard)
		checks = append(checks, doctorCheck{name: "java -version", ok: err == nil, detail: errorDetail(err, "runs successfully")})
	}

	failures := 0
	for _, check := range checks {
		status := "ok"
		if !check.ok {
			status = "fail"
			failures++
		}
		if err := app.outputf("[%s] %-15s %s\n", status, check.name, check.detail); err != nil {
			return err
		}
	}
	if failures > 0 {
		return fmt.Errorf("doctor found %d problem(s)", failures)
	}
	return nil
}

type doctorCheck struct {
	name   string
	ok     bool
	detail string
}

func (app application) activeJDK() (jdk.Installed, error) {
	active, _, err := app.activeJDKWithIndex()
	return active, err
}

func (app application) activeJDKWithIndex() (jdk.Installed, int, error) {
	installed, err := app.store.List()
	if err != nil {
		return jdk.Installed{}, 0, err
	}
	for index, installation := range installed {
		if installation.Active {
			return installation, index + 1, nil
		}
	}
	return jdk.Installed{}, 0, fmt.Errorf("no active JDK; run 'jvm-switcher switch <version>'")
}

func errorDetail(err error, success string) string {
	if err != nil {
		return err.Error()
	}
	return success
}

func (app application) outputf(format string, arguments ...any) error {
	if _, err := fmt.Fprintf(app.out, format, arguments...); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	return nil
}

func environmentValue(environment []string, name string) string {
	for _, variable := range environment {
		key, value, ok := strings.Cut(variable, "=")
		if ok && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func pathContains(pathValue, expected string) bool {
	for _, entry := range filepath.SplitList(pathValue) {
		if samePath(entry, expected) {
			return true
		}
	}
	return false
}

func samePath(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	left, _ = filepath.Abs(left)
	right, _ = filepath.Abs(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func resolveRemote(releases []jdk.Remote, selector string) (jdk.Remote, error) {
	for _, release := range releases {
		if release.ID() == selector {
			return release, nil
		}
	}
	var matches []jdk.Remote
	for _, release := range releases {
		distributionFeature := release.Distribution + "@" + strconv.Itoa(release.Feature)
		if release.Version == selector || strconv.Itoa(release.Feature) == selector || distributionFeature == selector {
			matches = append(matches, release)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return jdk.Remote{}, fmt.Errorf("JDK %q is ambiguous; use a distribution@version identifier or list index", selector)
	}
	index, err := strconv.Atoi(selector)
	if err == nil && index >= 1 && index <= len(releases) {
		return releases[index-1], nil
	}
	return jdk.Remote{}, fmt.Errorf("JDK %q is not available; run 'jvm-switcher show'", selector)
}

func displayDistribution(distribution string) string {
	if distribution == "" {
		return "-"
	}
	return distribution
}

func canonicalCommand(command string) string {
	switch command {
	case "ls":
		return "list"
	case "i":
		return "install"
	case "s":
		return "switch"
	case "rm":
		return "remove"
	case "h":
		return "help"
	default:
		return command
	}
}

// progressWriter reports download progress on a single rewritten line.
type progressWriter struct {
	out        io.Writer
	total      int64
	interval   time.Duration
	written    int64
	lastReport time.Time
	reported   bool
}

func (writer *progressWriter) Write(data []byte) (int, error) {
	writer.written += int64(len(data))
	if time.Since(writer.lastReport) >= writer.interval {
		writer.lastReport = time.Now()
		writer.reported = true
		if _, err := fmt.Fprintf(writer.out, "\r  %s", writer.status()); err != nil {
			return len(data), fmt.Errorf("write download progress: %w", err)
		}
	}
	return len(data), nil
}

func (writer *progressWriter) finish() error {
	if writer.reported {
		if _, err := fmt.Fprintf(writer.out, "\r  %s\n", writer.status()); err != nil {
			return fmt.Errorf("write download progress: %w", err)
		}
	}
	return nil
}

func (writer *progressWriter) status() string {
	if writer.total > 0 {
		percentage := 100 * float64(writer.written) / float64(writer.total)
		return fmt.Sprintf("%s of %s (%.0f%%)    ", humanSize(writer.written), humanSize(writer.total), percentage)
	}
	return fmt.Sprintf("%s    ", humanSize(writer.written))
}

func humanSize(size int64) string {
	if size <= 0 {
		return "-"
	}
	const unit = int64(1024)
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	divisor, exponent := unit, 0
	for quotient := size / unit; quotient >= unit; quotient /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(size)/float64(divisor), "KMGTPE"[exponent])
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		if code, ok := childExitCode(err); ok {
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func childExitCode(err error) (int, bool) {
	if exitError, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitError.ExitCode(), true
	}
	return 0, false
}
