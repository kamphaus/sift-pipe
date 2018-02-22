// sift
// Copyright (C) 2014-2016 Sven Taute
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, version 3 of the License.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package sift

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/svent/go-nbreader"
	"github.com/svent/sift/gitignore"
	"github.com/tevino/abool"
)

const (
	// InputMultilineWindow is the size of the sliding window for multiline matching
	InputMultilineWindow = 32 * 1024
	// MultilinePipeTimeout is the timeout for reading and matching input
	// from STDIN/network in multiline mode
	MultilinePipeTimeout = 1000 * time.Millisecond
	// MultilinePipeChunkTimeout is the timeout to consider last input from STDIN/network
	// as a complete chunk for multiline matching
	MultilinePipeChunkTimeout = 150 * time.Millisecond
	// MaxDirRecursionRoutines is the maximum number of parallel routines used
	// to recurse into directories
	MaxDirRecursionRoutines = 3
	SiftConfigFile          = ".sift.conf"
	SiftVersion             = "0.9.0"
)

type ConditionType int

const (
	ConditionPreceded ConditionType = iota
	ConditionFollowed
	ConditionSurrounded
	ConditionFileMatches
	ConditionLineMatches
	ConditionRangeMatches
)

type Condition struct {
	regex          *regexp.Regexp
	conditionType  ConditionType
	within         int64
	lineRangeStart int64
	lineRangeEnd   int64
	negated        bool
}

type FileType struct {
	Patterns     []string
	ShebangRegex *regexp.Regexp
}

type Match struct {
	// offset of the start of the match
	start int64
	// offset of the end of the match
	end int64
	// offset of the beginning of the first line of the match
	lineStart int64
	// offset of the end of the last line of the match
	lineEnd int64
	// the match
	match string
	// the match including the non-matched text on the first and last line
	line string
	// the line number of the beginning of the match
	lineno int64
	// the index to s.conditions (if this match belongs to a condition)
	conditionID int
	// the context before the match
	contextBefore *string
	// the context after the match
	contextAfter *string
}

type Matches []Match

func (e Matches) Len() int           { return len(e) }
func (e Matches) Swap(i, j int)      { e[i], e[j] = e[j], e[i] }
func (e Matches) Less(i, j int) bool { return e[i].start < e[j].start }

type Result struct {
	conditionMatches Matches
	matches          Matches
	// if too many matches are found or input is read only from STDIN,
	// matches are streamed through a channel
	matchChan chan Matches
	streaming bool
	isBinary  bool
	target    string
}

var (
	errLineTooLong = errors.New("line too long")
)

type Search struct {
	options               *Options
	errorLogger           *log.Logger
	inputBlockSize        int
	conditions            []Condition
	filesChan             chan string
	directoryChan         chan string
	fileTypesMap          map[string]FileType
	includeFilepathRegex  *regexp.Regexp
	excludeFilepathRegex  *regexp.Regexp
	netTcpRegex           *regexp.Regexp
	inputFile             io.ReadCloser
	outputFile            io.Writer
	matchPatterns         []string
	matchRegexes          []*regexp.Regexp
	gitignoreCache        *gitignore.GitIgnoreCache
	resultsChan           chan *Result
	resultsDoneChan       chan struct{}
	targetsWaitGroup      sync.WaitGroup
	recurseWaitGroup      sync.WaitGroup
	streamingAllowed      bool
	streamingThreshold    int
	termHighlightFilename string
	termHighlightLineno   string
	termHighlightMatch    string
	termHighlightReset    string
	totalLineLengthErrors int64
	totalMatchCount       int64
	totalResultCount      int64
	totalTargetCount      int64
	skipBytes             int64
	pipeAbort             *abool.AtomicBool
}

func (s *Search) GetOptions() *Options {
	return s.options
}

func (s *Search) GetMatchPatterns() []string {
	return s.matchPatterns
}

func NewSearch(options *Options, inputFile io.ReadCloser, outputFile io.Writer, errorLogger *log.Logger) *Search {
	var stdOut io.Writer = os.Stdout
	if outputFile != nil {
		stdOut = outputFile
	}
	var stdIn io.ReadCloser = os.Stdin
	if inputFile != nil {
		stdIn = inputFile
	}
	var errOut *log.Logger
	if errorLogger != nil {
		errOut = errorLogger
	} else {
		errOut = log.New(os.Stderr, "Error: ", 0)
	}
	return &Search{
		options:            options,
		inputFile:          stdIn,
		outputFile:         stdOut,
		errorLogger:        errOut,
		netTcpRegex:        regexp.MustCompile(`^(tcp[46]?)://(.*:\d+)$`),
		streamingThreshold: 1 << 16,
		fileTypesMap:       getFileTypes(),
		inputBlockSize:     256 * 1024,
		pipeAbort:          abool.New(),
		skipBytes:          options.SkipBytes,
	}
}

// processDirectories reads s.directoryChan and processes
// directories via processDirectory.
func (s *Search) processDirectories() {
	n := s.options.Cores
	if n > MaxDirRecursionRoutines {
		n = MaxDirRecursionRoutines
	}
	for i := 0; i < n; i++ {
		go func() {
			for dirname := range s.directoryChan {
				s.processDirectory(dirname)
			}
		}()
	}
}

// enqueueDirectory enqueues directories on s.directoryChan.
// If the channel blocks, the directory is processed directly.
func (s *Search) enqueueDirectory(dirname string) {
	s.recurseWaitGroup.Add(1)
	select {
	case s.directoryChan <- dirname:
	default:
		s.processDirectory(dirname)
	}
}

// processDirectory recurses into a directory and sends all files
// fulfilling the selected options on s.filesChan
func (s *Search) processDirectory(dirname string) {
	defer s.recurseWaitGroup.Done()
	var gic *gitignore.Checker
	if s.options.Git {
		gic = gitignore.NewCheckerWithCache(s.gitignoreCache)
		err := gic.LoadBasePath(dirname)
		if err != nil {
			s.errorLogger.Printf("cannot load gitignore files for path '%s': %s", dirname, err)
		}
	}
	dir, err := os.Open(dirname)
	if err != nil {
		s.errorLogger.Printf("cannot open directory '%s': %s\n", dirname, err)
		return
	}
	defer dir.Close()
	for {
		entries, err := dir.Readdir(256)
		if err == io.EOF {
			return
		}
		if err != nil {
			s.errorLogger.Printf("cannot read directory '%s': %s\n", dirname, err)
			return
		}

	nextEntry:
		for _, fi := range entries {
			fullpath := filepath.Join(dirname, fi.Name())

			// check directory include/exclude options
			if fi.IsDir() {
				if !s.options.Recursive {
					continue nextEntry
				}
				for _, dirPattern := range s.options.ExcludeDirs {
					matched, err := filepath.Match(dirPattern, fi.Name())
					if err != nil {
						s.errorLogger.Fatalf("cannot match malformed pattern '%s' against directory name: %s\n", dirPattern, err)
					}
					if matched {
						continue nextEntry
					}
				}
				if len(s.options.IncludeDirs) > 0 {
					for _, dirPattern := range s.options.IncludeDirs {
						matched, err := filepath.Match(dirPattern, fi.Name())
						if err != nil {
							s.errorLogger.Fatalf("cannot match malformed pattern '%s' against directory name: %s\n", dirPattern, err)
						}
						if matched {
							goto includeDirMatchFound
						}
					}
					continue nextEntry
				includeDirMatchFound:
				}
				if s.options.Git {
					if fi.Name() == gitignore.GitFoldername || gic.Check(fullpath, fi) {
						continue nextEntry
					}
				}
				s.enqueueDirectory(fullpath)
				continue nextEntry
			}

			// check whether this is a regular file
			if fi.Mode()&os.ModeType != 0 {
				if s.options.FollowSymlinks && fi.Mode()&os.ModeType == os.ModeSymlink {
					realPath, err := filepath.EvalSymlinks(fullpath)
					if err != nil {
						s.errorLogger.Printf("cannot follow symlink '%s': %s\n", fullpath, err)
					} else {
						realFi, err := os.Stat(realPath)
						if err != nil {
							s.errorLogger.Printf("cannot follow symlink '%s': %s\n", fullpath, err)
						}
						if realFi.IsDir() {
							s.enqueueDirectory(realPath)
							continue nextEntry
						} else {
							if realFi.Mode()&os.ModeType != 0 {
								continue nextEntry
							}
						}
					}
				} else {
					continue nextEntry
				}
			}

			// check file path options
			if s.excludeFilepathRegex != nil {
				if s.excludeFilepathRegex.MatchString(fullpath) {
					continue nextEntry
				}
			}
			if s.includeFilepathRegex != nil {
				if !s.includeFilepathRegex.MatchString(fullpath) {
					continue nextEntry
				}
			}

			// check file extension options
			if len(s.options.ExcludeExtensions) > 0 {
				for _, e := range strings.Split(s.options.ExcludeExtensions, ",") {
					if filepath.Ext(fi.Name()) == "."+e {
						continue nextEntry
					}
				}
			}
			if len(s.options.IncludeExtensions) > 0 {
				for _, e := range strings.Split(s.options.IncludeExtensions, ",") {
					if filepath.Ext(fi.Name()) == "."+e {
						goto includeExtensionFound
					}
				}
				continue nextEntry
			includeExtensionFound:
			}

			// check file include/exclude options
			for _, filePattern := range s.options.ExcludeFiles {
				matched, err := filepath.Match(filePattern, fi.Name())
				if err != nil {
					s.errorLogger.Fatalf("cannot match malformed pattern '%s' against file name: %s\n", filePattern, err)
				}
				if matched {
					continue nextEntry
				}
			}
			if len(s.options.IncludeFiles) > 0 {
				for _, filePattern := range s.options.IncludeFiles {
					matched, err := filepath.Match(filePattern, fi.Name())
					if err != nil {
						s.errorLogger.Fatalf("cannot match malformed pattern '%s' against file name: %s\n", filePattern, err)
					}
					if matched {
						goto includeFileMatchFound
					}
				}
				continue nextEntry
			includeFileMatchFound:
			}

			// check file type options
			if len(s.options.ExcludeTypes) > 0 {
				for _, t := range strings.Split(s.options.ExcludeTypes, ",") {
					for _, filePattern := range s.fileTypesMap[t].Patterns {
						if matched, _ := filepath.Match(filePattern, fi.Name()); matched {
							continue nextEntry
						}
					}
					sr := s.fileTypesMap[t].ShebangRegex
					if sr != nil {
						if m, err := checkShebang(sr, fullpath); m && err == nil {
							continue nextEntry
						}
					}
				}
			}
			if len(s.options.IncludeTypes) > 0 {
				for _, t := range strings.Split(s.options.IncludeTypes, ",") {
					for _, filePattern := range s.fileTypesMap[t].Patterns {
						if matched, _ := filepath.Match(filePattern, fi.Name()); matched {
							goto includeTypeFound
						}
					}
					sr := s.fileTypesMap[t].ShebangRegex
					if sr != nil {
						if m, err := checkShebang(sr, fullpath); err != nil || m {
							goto includeTypeFound
						}
					}
				}
				continue nextEntry
			includeTypeFound:
			}

			if s.options.Git {
				if fi.Name() == gitignore.GitIgnoreFilename || gic.Check(fullpath, fi) {
					continue
				}
			}

			s.filesChan <- fullpath
		}
	}
}

// checkShebang checks whether the first line of file matches the given regex
func checkShebang(regex *regexp.Regexp, filepath string) (bool, error) {
	f, err := os.Open(filepath)
	defer f.Close()
	if err != nil {
		return false, err
	}
	b, err := bufio.NewReader(f).ReadBytes('\n')
	return regex.Match(b), nil
}

// processFileTargets reads filesChan, builds an io.Reader for the target and calls processReader
func (s *Search) processFileTargets() {
	defer s.targetsWaitGroup.Done()
	dataBuffer := make([]byte, s.inputBlockSize)
	testBuffer := make([]byte, s.inputBlockSize)
	matchRegexes := make([]*regexp.Regexp, len(s.matchPatterns))
	for i := range s.matchPatterns {
		matchRegexes[i] = regexp.MustCompile(s.matchPatterns[i])
	}

	for filepath := range s.filesChan {
		var err error
		var infile *os.File
		var reader io.Reader

		if s.options.TargetsOnly {
			s.resultsChan <- &Result{target: filepath}
			continue
		}

		if filepath == "-" {
			if stdIn, ok := s.inputFile.(*os.File); ok {
				infile = stdIn
			} else {
				reader = s.inputFile
			}
		} else {
			infile, err = os.Open(filepath)
			if err != nil {
				s.errorLogger.Printf("cannot open file '%s': %s\n", filepath, err)
				continue
			}
		}

		if s.options.Zip && strings.HasSuffix(filepath, ".gz") {
			rawReader := infile
			reader, err = gzip.NewReader(rawReader)
			if err != nil {
				s.errorLogger.Printf("error decompressing file '%s', opening as normal file\n", infile.Name())
				infile.Seek(0, 0)
				reader = infile
			}
		} else if infile == os.Stdin && s.options.Multiline {
			reader = nbreader.NewNBReader(infile, s.inputBlockSize,
				nbreader.ChunkTimeout(MultilinePipeChunkTimeout), nbreader.Timeout(MultilinePipeTimeout))
		} else if infile != nil {
			reader = infile
		}

		if s.options.InvertMatch {
			err = s.processReaderInvertMatch(reader, matchRegexes, filepath)
		} else {
			err = s.processReader(reader, matchRegexes, dataBuffer, testBuffer, filepath)
		}
		if err != nil {
			if err == errLineTooLong {
				s.totalLineLengthErrors += 1
				if s.options.ErrShowLineLength {
					errmsg := fmt.Sprintf("file contains very long lines (>= %d bytes). See options --blocksize and --err-skip-line-length.", s.inputBlockSize)
					s.errorLogger.Printf("cannot process data from file '%s': %s\n", filepath, errmsg)
				}
			} else {
				s.errorLogger.Printf("cannot process data from file '%s': %s\n", filepath, err)
			}
		}
		infile.Close()
	}
}

// processNetworkTarget starts a listening TCP socket and calls processReader
func (s *Search) processNetworkTarget(target string) {
	matchRegexes := make([]*regexp.Regexp, len(s.matchPatterns))
	for i := range s.matchPatterns {
		matchRegexes[i] = regexp.MustCompile(s.matchPatterns[i])
	}
	defer s.targetsWaitGroup.Done()

	var reader io.Reader
	netParams := s.netTcpRegex.FindStringSubmatch(target)
	proto := netParams[1]
	addr := netParams[2]

	listener, err := net.Listen(proto, addr)
	if err != nil {
		s.errorLogger.Fatalf("could not listen on '%s'\n", target)
	}

	conn, err := listener.Accept()
	if err != nil {
		s.errorLogger.Fatalf("could not accept connections on '%s'\n", target)
	}

	if s.options.Multiline {
		reader = nbreader.NewNBReader(conn, s.inputBlockSize, nbreader.ChunkTimeout(MultilinePipeChunkTimeout),
			nbreader.Timeout(MultilinePipeTimeout))
	} else {
		reader = conn
	}

	dataBuffer := make([]byte, s.inputBlockSize)
	testBuffer := make([]byte, s.inputBlockSize)
	err = s.processReader(reader, matchRegexes, dataBuffer, testBuffer, target)
	if err != nil {
		s.errorLogger.Printf("error processing data from '%s'\n", target)
		return
	}
}

func (s *Search) ExecuteSearch(targets []string) (ret int, err error) {
	defer func() {
		if r := recover(); r != nil {
			ret = 2
			err = errors.New(r.(string))
		}
	}()
	tstart := time.Now()
	s.filesChan = make(chan string, 256)
	s.directoryChan = make(chan string, 128)
	s.resultsChan = make(chan *Result, 128)
	s.resultsDoneChan = make(chan struct{})
	s.gitignoreCache = gitignore.NewGitIgnoreCache()
	s.totalTargetCount = 0
	s.totalLineLengthErrors = 0
	s.totalMatchCount = 0
	s.totalResultCount = 0

	go s.resultHandler()

	for i := 0; i < s.options.Cores; i++ {
		s.targetsWaitGroup.Add(1)
		go s.processFileTargets()
	}

	go s.processDirectories()

	for _, target := range targets {
		switch {
		case target == "-":
			s.filesChan <- "-"
		case s.netTcpRegex.MatchString(target):
			s.targetsWaitGroup.Add(1)
			go s.processNetworkTarget(target)
		default:
			fileinfo, err := os.Stat(target)
			if err != nil {
				if os.IsNotExist(err) {
					s.errorLogger.Fatalf("no such file or directory: %s\n", target)
				} else {
					s.errorLogger.Fatalf("cannot open file or directory: %s\n", target)
				}
			}
			if fileinfo.IsDir() {
				s.recurseWaitGroup.Add(1)
				s.directoryChan <- target
			} else {
				s.filesChan <- target
			}
		}
	}

	s.recurseWaitGroup.Wait()
	close(s.directoryChan)

	close(s.filesChan)
	s.targetsWaitGroup.Wait()

	close(s.resultsChan)
	<-s.resultsDoneChan

	var retVal int
	if s.totalResultCount > 0 {
		retVal = 0
	} else {
		retVal = 1
	}

	if !s.options.ErrSkipLineLength && !s.options.ErrShowLineLength && s.totalLineLengthErrors > 0 {
		s.errorLogger.Printf("%d files skipped due to very long lines (>= %d bytes). See options --blocksize, --err-show-line-length and --err-skip-line-length.", s.totalLineLengthErrors, s.inputBlockSize)
	}

	if s.options.Stats {
		tend := time.Now()
		fmt.Fprintln(os.Stderr, s.totalTargetCount, "files processed")
		fmt.Fprintln(os.Stderr, s.totalResultCount, "files match")
		fmt.Fprintln(os.Stderr, s.totalMatchCount, "matches found")
		fmt.Fprintf(os.Stderr, "in %v\n", tend.Sub(tstart))
	}

	s.inputFile.Close()

	return retVal, nil
}
