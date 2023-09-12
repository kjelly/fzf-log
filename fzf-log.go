package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	isatty "github.com/mattn/go-isatty"

	"github.com/gravwell/gravwell/v3/timegrinder"
	"github.com/jessevdk/go-flags"
	fuzzyfinder "github.com/ktr0731/go-fuzzyfinder"
	funk "github.com/thoas/go-funk"
)

func GoMap[T interface{}, U interface{}](values []U, f func(index int, value U) []T) []T {
	if len(values) == 0 {
		return []T{}
	}
	c := make(chan []T, 10)
	for i, v := range values {
		go func(i int, c chan []T, v U) {
			c <- f(i, v)
		}(i, c, v)
	}
	count := 0
	var result []T
	for i := range c {
		count += 1
		if count >= len(values) {
			close(c)
		}
		result = append(result, i...)
	}
	return result
}

type Options struct {
	Command    []string `short:"c" long:"command" description:"command output"`
	TempPrefix string   `short:"t" long:"temp-file" description:"temp file prefix" default:"tmp"`
	TempDir    string   `long:"temp-dir" description:"temp file prefix" default:"/tmp"`
	Editor     string   `long:"editor" description:"editor path" default:"less"`
	Before     string   `long:"before" description:"before"`
	After      string   `long:"after" description:"after"`
	SkipColumn int      `long:"skip-column" description:"skip column" default:"0"`
	Ago        string   `long:"ago" description:"seconds ago"`
	Limit      int      `long:"limit" short:"l" description:"limit" default:"10000"`

	Args struct {
		Rest []string
	} `positional-args:"yes" required:"yes"`
}

type LogRecord struct {
	Time    time.Time
	Content string
	File    string
	Line    int
}

func use(interface{}) {}

func getLines(file string) []string {
	var logs []LogRecord
	use(logs)
	var fileLines []string

	readFile, err := os.Open(file)
	if err != nil {
		log.Fatal(err)
	}
	fileScanner := bufio.NewScanner(readFile)
	fileScanner.Split(bufio.ScanLines)
	for fileScanner.Scan() {
		fileLines = append(fileLines, fileScanner.Text())
	}
	return fileLines
}

func StrToDate(s string) *time.Time {
	t, ok, err := timegrinder.Extract([]byte(s))
	if !ok {
		return nil
	}
	if err != nil {
		return nil
	}
	if t.Year() == 0 {
		t = t.AddDate(time.Now().Year(), 0, 0)
	}
	return &t
}

func ParseFileToLogRecords(file string, lines []string, toDate func(string) *time.Time) []LogRecord {
	var logs []LogRecord
	logs = GoMap(lines, func(i int, line string) []LogRecord {
		t := toDate(line)
		if t == nil {
			return []LogRecord{}
		} else {
			return []LogRecord{{Content: "", Line: i, File: file, Time: *t}}
		}
	})
	return logs
}

func System(cmd string) string {
	out, err := exec.Command("bash", "-c", cmd).Output()
	if err != nil {
		log.Printf("command(%s) failed: %v\n", cmd, err)
		return ""
	}
	return string(out)
}

func main() {
	// pflag.Parse()
	var opts Options
	_, err := flags.Parse(&opts)
	switch flagsErr := err.(type) {
	case flags.ErrorType:
		if flagsErr == flags.ErrHelp {
			os.Exit(0)
		}
		os.Exit(1)
	case nil:
		// no error
	default:
		os.Exit(1)
	}

	lineMap := map[string][]string{}
	pathList := opts.Args.Rest

	pathList = append(pathList, GoMap(opts.Command, func(i int, cmd string) []string {
		path := filepath.Join(opts.TempDir, opts.TempPrefix+"-"+cmd)
		content := System(cmd)
		if content == "" {
			return []string{}
		}
		f, err := os.Create(path)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		_, err = f.WriteString(content)

		return []string{path}
	})...)

	if isatty.IsTerminal(os.Stdin.Fd()) {
	} else {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			log.Fatal(err)
		}
		content := string(b)
		l := strings.Split(content, "\n")
    path := filepath.Join(opts.TempDir, opts.TempPrefix+"-"+"stdin")
		// write to temp file
		f, err := os.Create(path)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		_, err = f.WriteString(content)
		pathList = append(pathList, path)
		lineMap[path] = l
	}
	fmt.Println(pathList)
	if len(pathList) == 0 {
		fmt.Println("no files")
		return
	}

	var logs []LogRecord
	logs = GoMap(pathList, func(i int, path string) []LogRecord {
		lines := getLines(path)
		lineMap[path] = lines
		return ParseFileToLogRecords(path, lines, StrToDate)
	})

	// sort by time
	sort.Slice(logs, func(i, j int) bool {
		return !logs[i].Time.Before(logs[j].Time)
	})

	// filter by date
	if opts.After != "" {
		start := StrToDate(opts.After)
		if start != nil {
			logs = funk.Filter(logs, func(log LogRecord) bool {
				return log.Time.After(*start)
			}).([]LogRecord)
		}
	}
	if opts.Before != "" {
		end := StrToDate(opts.Before)
		if end != nil {
			logs = funk.Filter(logs, func(log LogRecord) bool {
				return log.Time.Before(*end)
			}).([]LogRecord)
		}
	}

	if opts.Ago != "" {
		ago_time, err := time.ParseDuration(opts.Ago)
		if err != nil {
			fmt.Printf("ago %v is not a valid duration, %v\n", opts.Ago, err)
		} else {
			t := time.Now().Add(-ago_time)
			logs = funk.Filter(logs, func(log LogRecord) bool {
				return log.Time.After(t)
			}).([]LogRecord)
		}
	}

	if len(logs) == 0 {
		fmt.Printf("no logs\n")
		return
	}

	logs = logs[:min(opts.Limit, len(logs))]

	logsLength := len(logs)

	for i, log := range logs {
		end := log.Line + 1
		if i < logsLength-1 {
			end = logs[i+1].Line
		}
		logs[i].Content = GetRangeLines(lineMap[log.File], log.Line, end, " ", func(line string) string {
			parts := strings.Split(line, " ")
			if len(parts) < opts.SkipColumn {
				return ""
			}
			return strings.Join(parts[opts.SkipColumn:], " ")
		})
	}
	for {
		idx, err := fuzzyfinder.Find(
			logs,
			func(i int) string {
				return logs[i].Content
			},
			fuzzyfinder.WithPreviewWindow(func(i, w, h int) string {
				if i == -1 {
					return ""
				}
				start := logs[i].Line
				var end = logs[i].Line + 1
				if i+1 < logsLength {
					end = logs[i+1].Line + h
				}
				return GetRangeLines(lineMap[logs[i].File], start, end, "\n", nil)
			}))
		if err != nil {
			log.Fatal(err)
		}
    log := logs[idx]
		cmd := exec.Command(opts.Editor, fmt.Sprintf("+%d", log.Line), log.File)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Run()
	}
}

func GetRangeLines(lines []string, start int, end int, sep string, f func(string) string) string {
	linesLength := len(lines)
	if end > linesLength {
		end = linesLength
	}
	if start > end {
		start = end - 1
	}
	if start < 0 {
		start = 0
	}
	newLines := append([]string{}, (lines)[start:end]...)
	if len(newLines) == 0 {
		return ""
	}
	if f != nil {
		newLines[0] = f(newLines[0])
	}
	return strings.Join(newLines, sep)

}

func min[T int | float32](a, b T) T {
	if a < b {
		return a
	}
	return b
}
