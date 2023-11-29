package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/anmitsu/go-shlex"
	"golang.org/x/term"
	"tractor.dev/toolkit-go/engine"
	"tractor.dev/toolkit-go/engine/cli"
	"tractor.dev/toolkit-go/engine/fs"
	"tractor.dev/wanix/kernel/proc/exec"
)

func main() {
	engine.Run(Shell{})
}

type Shell struct {
	cmd          *cli.Command
	stdinRouter  *SwitchableWriter
	defaultStdin *BlockingBuffer
	script       *os.File
	lineNum      int
}

func (m *Shell) Initialize() {
	m.cmd = &cli.Command{}
	m.cmd.AddCommand(exitCmd())
	m.cmd.AddCommand(echoCmd())
	m.cmd.AddCommand(openCmd())
	m.cmd.AddCommand(mtimeCmd())
	m.cmd.AddCommand(lsCmd())
	m.cmd.AddCommand(cdCmd())
	m.cmd.AddCommand(catCmd())
	m.cmd.AddCommand(reloadCmd())
	m.cmd.AddCommand(downloadCmd())
	m.cmd.AddCommand(touchCmd())
	m.cmd.AddCommand(removeCmd())
	m.cmd.AddCommand(mkdirCmd())
	m.cmd.AddCommand(moveCmd())
	m.cmd.AddCommand(copyCmd())
	m.cmd.AddCommand(copyCmd2()) // temporary
	m.cmd.AddCommand(pwdCmd())
	m.cmd.AddCommand(writeCmd())
	m.cmd.AddCommand(printEnvCmd())
	m.cmd.AddCommand(exportCmd())
	m.cmd.AddCommand(treeCmd())
	m.cmd.Run = m.ExecuteCommand

	m.defaultStdin = NewBlockingBuffer()
	m.stdinRouter = &SwitchableWriter{writer: m.defaultStdin}

	go io.Copy(m.stdinRouter, os.Stdin)
}

func (m *Shell) Run(ctx context.Context) (err error) {
	var readLine func() (string, error)

	if len(os.Args) > 1 {
		if filepath.Ext(os.Args[1]) != ".sh" {
			return fmt.Errorf("script argument must be a '.sh' file")
		}
		m.script, err = os.Open(os.Args[1])
		if err != nil {
			return fmt.Errorf("unable to open script: %w", err)
		}
		scanner := bufio.NewScanner(m.script)
		readLine = func() (string, error) {
			if !scanner.Scan() {
				return "exit", scanner.Err()
			}
			return scanner.Text(), nil
		}

	} else {
		fmt.Println(`
    ____    _____  _____     ___    __      __   ____   _
|  |    |  |    /  \    |    \  |  | (_    _) \  \  /  / 
|  |    |  |   /    \   |  |\ \ |  |   |  |    \  \/  /  
|  |    |  |  /  ()  \  |  | \ \|  |   |  |     >    <   
 \  \/\/  /  |   __   | |  |  \    |  _|  |_   /  /\  \  
__\      /___|  (__)  |_|  |___\   |_(      )_/  /__\  \_
																																		
`)
		terminal := term.NewTerminal(struct {
			io.Reader
			io.Writer
		}{
			Reader: m.defaultStdin,
			Writer: os.Stdout,
		}, "/ ▶ ")
		// TODO: handle resizes
		readLine = func() (string, error) {
			wd, err := os.Getwd()
			if err != nil {
				panic(err)
			}
			terminal.SetPrompt(fmt.Sprintf("%s ▶ ", wd))
			return terminal.ReadLine()
		}
	}

	// if !js.Global().Get("account").IsUndefined() {
	// 	fmt.Fprintf(ch, "Signed in as %s.\n\n", js.Global().Get("account").Get("profile").Get("name").String())
	// }

	for {
		m.lineNum++

		line, err := readLine()
		if err != nil {
			m.printErr(fmt.Errorf("input error: %w", err))
			continue
		}

		args, err := shlex.Split(line, true)
		if err != nil {
			m.printErr(fmt.Errorf("parsing error: %w", err))
			continue
		}

		ctx := cli.ContextWithIO(context.Background(), m.defaultStdin, os.Stdout, os.Stderr)
		if err := cli.Execute(ctx, m.cmd, args); err != nil {
			m.printErr(fmt.Errorf("exec error: %w", err))
		}

		fmt.Println()
	}
}

func (m *Shell) ExecuteCommand(ctx *cli.Context, args []string) {
	env := make(map[string]string)
	for _, kvp := range os.Environ() {
		parts := strings.SplitN(kvp, "=", 2)
		env[parts[0]] = parts[1]
	}

	var err error
	args, err = parseEnvArgs(args, env)
	if err != nil {
		m.printErr(err)
		return
	}

	if len(args) == 0 {
		return
	}

	cmd, err := findCommand(args[0], args[1:])
	if err != nil {
		m.printErr(err)
		return
	}

	cmd.Env = packEnv(env)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	wc := cmd.StdinPipe()
	m.stdinRouter.Switch(wc)
	defer m.stdinRouter.Switch(m.defaultStdin)

	if _, err := cmd.Run(); err != nil {
		m.printErr(err)
	}

}

func (m *Shell) printErr(err error) {
	if m.script != nil {
		fmt.Printf("script error on line %d: %s\n", m.lineNum, err)
		return
	}
	fmt.Println(err)
}

func findCommand(name string, args []string) (*exec.Cmd, error) {
	fsys := os.DirFS("/")

	var (
		scriptPath string
		wasmPath   string
		buildPath  string
	)

	if !strings.Contains(name, "/") {
		// bare command: no path, no extension
		ext := filepath.Ext(name)
		cmdName := strings.TrimSuffix(name, ext)
		for _, path := range []string{"/cmd", "/sys/cmd", "/sys/bin"} {

			searchPath := filepath.Join(path, fmt.Sprintf("%s.wasm", cmdName))
			if ok, _ := fs.Exists(fsys, unixToFsPath(searchPath)); ok && isWasmFile(searchPath) {
				wasmPath = searchPath
				break
			}

			searchPath = filepath.Join(path, fmt.Sprintf("%s.sh", cmdName))
			if ok, _ := fs.Exists(fsys, unixToFsPath(searchPath)); ok {
				scriptPath = searchPath
				break
			}

			searchPath = filepath.Join(path, cmdName)
			if ok, _ := fs.DirExists(fsys, unixToFsPath(searchPath)); ok {
				if matches, _ := fs.Glob(fsys, fmt.Sprintf("%s/*.go", unixToFsPath(searchPath))); len(matches) > 0 {
					buildPath = searchPath
					break
				}
			}
		}
	} else {
		// absolute command: path and extension
		path := absPath(name)
		ext := filepath.Ext(path)
		switch ext {
		case ".wasm":
			if ok, _ := fs.Exists(fsys, unixToFsPath(path)); ok && isWasmFile(path) {
				wasmPath = path
			}
		case ".sh":
			if ok, _ := fs.Exists(fsys, unixToFsPath(path)); ok {
				scriptPath = path
			}
		default:
			if ok, _ := fs.DirExists(fsys, unixToFsPath(path)); ok {
				if matches, _ := fs.Glob(fsys, fmt.Sprintf("%s/*.go", unixToFsPath(path))); len(matches) > 0 {
					buildPath = path
				}
			}
		}
	}

	if scriptPath != "" {
		shellArgs := append([]string{scriptPath}, args...)
		// TODO: shell is currently only available in the initfs,
		// but the process worker is able to exec it from there anyway.
		// We should really mount the shell exe in /sys/bin though.
		return exec.Command("shell", shellArgs...), nil
	}

	var err error
	if buildPath != "" {
		wasmPath, err = buildCmdSource(buildPath)
		if err != nil {
			return nil, err
		}
	}

	if wasmPath != "" {
		return exec.Command(wasmPath, args...), nil
	}

	return nil, fmt.Errorf("unable to find command: %s", name)
}
