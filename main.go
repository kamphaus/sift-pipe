package main

import (
	"encoding/json"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/kamphaus/sift"
	"github.com/kamphaus/sift-pipe/types"
)

var (
	errorLogger = log.New(os.Stderr, "Error: ", 0)
)

func isInfiniStream(file string) bool {
	_, name := filepath.Split(file)
	return strings.Contains(name, "infini-stream")
}

func main() {
	var waitGroup sync.WaitGroup
	inb, err := ioutil.ReadAll(os.Stdin)
	if err != nil {
		panic(err)
	}
	//fmt.Println("Read from stdin: ", string(inb))
	var ms types.MultiSearch
	err = json.Unmarshal(inb, &ms)
	if err != nil {
		panic(err)
	}
	/*tmp, err := json.Marshal(ms)
	if err != nil {
		panic(err)
	}
	fmt.Println("Check: " + string(tmp))*/
	if len(ms.Search) == 0 {
		errorLogger.Fatalf("Nothing to search")
	}
	runtime.GOMAXPROCS(len(ms.Search))
	var cmd *exec.Cmd
	var input io.ReadCloser
	var output io.WriteCloser
	// for testing purpose: allow calling a different program that provides an infinite stream of strings
	if len(ms.Targets) == 1 && isInfiniStream(ms.Targets[0]) {
		cmd = exec.Command(ms.Targets[0])
		cmd.Stderr = os.Stderr
		if err != nil {
			panic(err)
		}
		input, err = cmd.StdoutPipe()
		if err != nil {
			panic(err)
		}
		err = cmd.Start()
		if err != nil {
			panic(err)
		}
		defer cmd.Process.Kill()
	}
	var rets = make([]int, len(ms.Search))
	for i, s := range ms.Search {
		pipeIn, pipeOut := io.Pipe()
		if i == len(ms.Search)-1 {
			output = os.Stdout
		} else {
			output = pipeOut
		}
		var targets []string
		showFilename := "off"
		if s.ShowFilename {
			showFilename = "on"
		}
		search := sift.NewSearch(&sift.Options{
			BinarySkip:        false,
			BinaryAsText:      false,
			Blocksize:         "",
			Color:             "off",
			Cores:             1,
			IncludeDirs:       nil,
			ErrShowLineLength: false,
			ErrSkipLineLength: false,
			ExcludeDirs:       nil,
			IncludeExtensions: "",
			ExcludeExtensions: "",
			IncludeFiles:      nil,
			ExcludeFiles:      nil,
			IncludePath:       "",
			IncludeIPath:      "",
			ExcludePath:       "",
			ExcludeIPath:      "",
			IncludeTypes:      "",
			ExcludeTypes:      "",
			CustomTypes:       nil,
			FieldSeparator:    ":",
			FilesWithMatches:  false,
			FilesWithoutMatch: false,
			FollowSymlinks:    false,
			Git:               false,
			GroupByFile:       false,
			IgnoreCase:        false,
			SmartCase:         false,
			Limit:             s.Limit,
			Literal:           true,
			Multiline:         false,
			OutputLimit:       0,
			OutputUnixPath:    false,
			Recursive:         true,
			ShowFilename:      showFilename,
			ShowLineNumbers:   s.ShowLineNumbers,
			ShowColumnNumbers: false,
			ShowByteOffset:    s.ShowByteOffset,
			Stats:             s.Stats,
			TargetsOnly:       false,
			WordRegexp:        false,
			Zip:               false,
			Patterns:          []string{s.Pattern},
			SkipBytes:         s.SkipBytes,
		}, input, output, errorLogger)
		input = pipeIn

		if i == 0 {
			if len(ms.Targets) == 1 && isInfiniStream(ms.Targets[0]) {
				targets = search.ProcessArgs([]string{"-"})
			} else {
				targets = search.ProcessArgs(ms.Targets)
			}
		} else {
			targets = search.ProcessArgs([]string{"-"})
		}

		if err := search.Apply(search.GetMatchPatterns(), targets); err != nil {
			errorLogger.Fatalf("cannot process options: %s\n", err)
		}

		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			defer pipeIn.Close()
			defer pipeOut.Close()
			retVal, err := search.ExecuteSearch(targets)
			if err != nil {
				errorLogger.Println(err)
			}
			rets[index] = retVal
		}(i)
	}
	waitGroup.Wait()
	for _, val := range rets {
		if val != 0 {
			os.Exit(val)
		}
	}
	os.Exit(0)
}
