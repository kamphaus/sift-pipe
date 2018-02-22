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
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

func (s *Search) resultHandler() {
	for result := range s.resultsChan {
		if s.options.TargetsOnly {
			fmt.Println(result.target)
			continue
		}
		s.totalTargetCount++
		s.applyConditions(result)
		s.printResult(result)
	}
	s.resultsDoneChan <- struct{}{}
}

func (s *Search) writeOutput(format string, a ...interface{}) {
	output := fmt.Sprintf(format, a...)
	_, err := s.outputFile.Write([]byte(output))
	if err != nil {
		if err != io.ErrClosedPipe {
			s.errorLogger.Fatalln("cannot write to output file:", err)
		} else {
			s.pipeAbort.Set()
		}
	}
}

func (s *Search) printFilename(filename string, delim string) {
	var options = s.options
	if options.ShowFilename == "on" && !options.GroupByFile {
		if options.OutputUnixPath {
			filename = filepath.ToSlash(filename)
		}
		s.writeOutput(s.termHighlightFilename+"%s"+s.termHighlightReset+delim, filename)
	}
}

func (s *Search) printLineno(lineno int64, delim string) {
	if s.options.ShowLineNumbers {
		s.writeOutput(s.termHighlightLineno+"%d"+s.termHighlightReset+delim, lineno)
	}
}

func (s *Search) printColumnNo(m *Match) {
	var options = s.options
	if options.ShowColumnNumbers {
		s.writeOutput("%d"+options.FieldSeparator, m.start-m.lineStart+1)
	}
}

func (s *Search) printByteOffset(m *Match) {
	var options = s.options
	if options.ShowByteOffset {
		if options.OnlyMatching {
			s.writeOutput("%d"+options.FieldSeparator, m.start)
		} else {
			s.writeOutput("%d"+options.FieldSeparator, m.lineStart)
		}
	}
}

// printMatch prints the context after the previous match, the context before the match and the match itself
func (s *Search) printMatch(match Match, lastMatch Match, target string, lastPrintedLine *int64) {
	var options = s.options
	var matchOutput = match.line

	if !options.InvertMatch {
		if options.Replace != "" {
			matchOutput = match.match
			var matchTest string
			if options.IgnoreCase {
				tmp := []byte(match.match)
				for i := 0; i < len(tmp); i++ {
					bytesToLower(tmp, tmp, len(tmp))
				}
				matchTest = string(tmp)
			} else {
				matchTest = match.match
			}

			var res []byte
			for _, re := range s.matchRegexes {
				submatchIndexes := re.FindAllStringSubmatchIndex(matchTest, -1)
				if len(submatchIndexes) > 0 {
					for _, subIndex := range submatchIndexes {
						res = re.ExpandString(res, options.Replace, matchOutput, subIndex)
					}
					break
				}
			}

			matchOutput = string(res)
			if options.OutputLimit > 0 {
				var end int
				if options.OutputLimit > len(matchOutput) {
					end = len(matchOutput)
				} else {
					end = options.OutputLimit
				}
				matchOutput = matchOutput[0:end]
			}
		} else {
			// replace option not used
			if options.OutputLimit > 0 {
				var end int
				if options.OutputLimit > len(matchOutput) {
					end = len(matchOutput)
				} else {
					end = options.OutputLimit
				}
				matchOutput = matchOutput[0:end]
			}
			if options.Color == "on" {
				start := match.start - match.lineStart
				end := match.end - match.lineStart
				if int(end) <= len(matchOutput) {
					matchOutput = matchOutput[0:end] + s.termHighlightReset + matchOutput[end:]
					matchOutput = matchOutput[0:start] + s.termHighlightMatch + matchOutput[start:]
				}
			}
		}
	}

	// print contextAfter of the previous match
	contextBlockIncomplete := false
	if lastMatch.contextAfter != nil {
		contextLines := strings.Split(*lastMatch.contextAfter, "\n")
		for index, line := range contextLines {
			var lineno int64
			if options.Multiline {
				multilineLineCount := len(strings.Split(lastMatch.line, "\n")) - 1
				lineno = lastMatch.lineno + int64(index) + 1 + int64(multilineLineCount)
			} else {
				lineno = lastMatch.lineno + int64(index) + 1
			}
			// line is not part of the current match
			if lineno < match.lineno {
				s.printFilename(target, "-")
				s.printLineno(lineno, "-")
				s.writeOutput("%s\n", line)
				*lastPrintedLine = lineno
			} else {
				contextBlockIncomplete = true
			}
		}
	}
	if (lastMatch.contextAfter != nil || match.contextBefore != nil) && !contextBlockIncomplete {
		if match.lineno-int64(options.ContextBefore) > *lastPrintedLine+1 {
			// at least one line between the contextAfter of the previous match and the contextBefore of the current match
			fmt.Fprintln(s.outputFile, "--")
		}
	}

	// print contextBefore of the current match
	if match.contextBefore != nil {
		contextLines := strings.Split(*match.contextBefore, "\n")
		for index, line := range contextLines {
			lineno := match.lineno - int64(len(contextLines)) + int64(index)
			if lineno > *lastPrintedLine {
				s.printFilename(target, "-")
				s.printLineno(lineno, "-")
				s.writeOutput("%s\n", line)
				*lastPrintedLine = lineno
			}
		}
	}

	// print current match
	if options.Multiline {
		lines := strings.Split(match.line, "\n")
		if len(lines) > 1 && options.Replace == "" {
			firstLine := lines[0]
			lastLine := lines[len(lines)-1]
			firstLineOffset := match.start - match.lineStart
			lastLineOffset := int64(len(lastLine)) - (match.lineEnd - match.end)

			// first line of multiline match with partial highlighting
			s.printFilename(target, options.FieldSeparator)
			s.printLineno(match.lineno, options.FieldSeparator)
			s.printColumnNo(&match)
			s.printByteOffset(&match)
			s.writeOutput("%s%s%s%s\n", firstLine[0:firstLineOffset], s.termHighlightMatch,
				firstLine[firstLineOffset:], s.termHighlightReset)

			// lines 2 upto n-1 of multiline match with full highlighting
			for i := 1; i < len(lines)-1; i++ {
				line := lines[i]
				s.printFilename(target, options.FieldSeparator)
				s.printLineno(match.lineno+int64(i), options.FieldSeparator)
				s.writeOutput("%s%s%s\n", s.termHighlightMatch, line, s.termHighlightReset)
			}

			// last line of multiline match with partial highlighting
			s.printFilename(target, options.FieldSeparator)
			s.printLineno(match.lineno+int64(len(lines))-1, options.FieldSeparator)
			s.writeOutput("%s%s%s%s%s", s.termHighlightMatch, lastLine[0:lastLineOffset],
				s.termHighlightReset, lastLine[lastLineOffset:], options.OutputSeparator)
			*lastPrintedLine = match.lineno + int64(len(lines)-1)
		} else {
			// single line output in multiline mode or replace option used
			s.printFilename(target, options.FieldSeparator)
			s.printLineno(match.lineno, options.FieldSeparator)
			s.printColumnNo(&match)
			s.printByteOffset(&match)
			s.writeOutput("%s%s", matchOutput, options.OutputSeparator)
			*lastPrintedLine = match.lineno + int64(len(lines)-1)
		}
	} else {
		// single line output
		s.printFilename(target, options.FieldSeparator)
		s.printLineno(match.lineno, options.FieldSeparator)
		s.printColumnNo(&match)
		s.printByteOffset(&match)
		s.writeOutput("%s%s", matchOutput, options.OutputSeparator)
		*lastPrintedLine = match.lineno
	}
}

// printResult prints results using s.printMatch and handles various output options.
func (s *Search) printResult(result *Result) {
	var options = s.options
	var matchCount int64
	target := result.target
	matches := result.matches
	if options.FilesWithoutMatch {
		if len(matches) == 0 {
			s.writeOutput("%s\n", target)
			s.totalResultCount++
		}
		return
	}
	if options.FilesWithMatches && !options.Count {
		if len(matches) > 0 {
			s.writeOutput("%s\n", target)
			s.totalMatchCount++
			s.totalResultCount++
		}
		return
	}
	if options.Count {
		matchCount = int64(len(matches))
		if options.Limit != 0 && matchCount > options.Limit {
			matchCount = options.Limit
		}
		if result.streaming {
		countingMatchesLoop:
			for matches := range result.matchChan {
				matchCount += int64(len(matches))
				if options.Limit != 0 && matchCount >= options.Limit {
					matchCount = options.Limit
					break countingMatchesLoop
				}
			}
		}
		if options.FilesWithMatches {
			if matchCount > 0 {
				s.writeOutput("%s"+options.FieldSeparator+"%d\n", target, matchCount)
			}
		} else {
			if options.ShowFilename == "on" {
				s.writeOutput("%s"+options.FieldSeparator, target)
			}
			s.writeOutput("%d\n", matchCount)
		}
		s.totalMatchCount += matchCount
		if matchCount > 0 {
			s.totalResultCount++
		}
		return
	}

	if len(matches) == 0 {
		return
	}

	// print separator between file results if this is not the first result
	if s.totalMatchCount > 0 {
		if options.GroupByFile {
			fmt.Fprintln(s.outputFile, "")
		} else {
			if options.ContextBefore > 0 || options.ContextAfter > 0 {
				fmt.Fprintln(s.outputFile, "--")
			}
		}
	}

	if result.isBinary && !options.BinarySkip && !options.BinaryAsText {
		filename := result.target
		if options.OutputUnixPath {
			filename = filepath.ToSlash(filename)
		}
		s.writeOutput("Binary file matches: %s\n", filename)
		s.totalMatchCount++
		s.totalResultCount++
		return
	}

	if options.GroupByFile {
		filename := result.target
		if options.OutputUnixPath {
			filename = filepath.ToSlash(filename)
		}
		s.writeOutput(s.termHighlightFilename+"%s\n"+s.termHighlightReset, filename)
	}

	var lastPrintedLine int64 = -1
	var lastMatch Match

	// print contextBefore of first match
	if m := matches[0]; m.contextBefore != nil {
		contextLines := strings.Split(*m.contextBefore, "\n")
		for index, line := range contextLines {
			lineno := m.lineno - int64(len(contextLines)) + int64(index)
			s.printFilename(result.target, "-")
			s.printLineno(lineno, "-")
			s.writeOutput("%s\n", line)
			lastPrintedLine = lineno
		}
	}

	// print matches with their context
	lastMatch = matches[0]
	for _, match := range matches {
		s.printMatch(match, lastMatch, result.target, &lastPrintedLine)
		lastMatch = match
		matchCount++
		if options.Limit != 0 && matchCount >= options.Limit {
			break
		}
	}
	if result.streaming {
	matchStreamLoop:
		for matches := range result.matchChan {
			for _, match := range matches {
				s.printMatch(match, lastMatch, result.target, &lastPrintedLine)
				lastMatch = match
				matchCount++
				if options.Limit != 0 && matchCount >= options.Limit {
					break matchStreamLoop
				}
			}
		}
	}

	// print contextAfter of last match
	if lastMatch.contextAfter != nil {
		contextLines := strings.Split(*lastMatch.contextAfter, "\n")
		for index, line := range contextLines {
			var lineno int64
			if options.Multiline {
				multilineLineCount := len(strings.Split(lastMatch.line, "\n")) - 1
				lineno = lastMatch.lineno + int64(index) + 1 + int64(multilineLineCount)
			} else {
				lineno = lastMatch.lineno + int64(index) + 1
			}
			s.printFilename(result.target, "-")
			s.printLineno(lineno, "-")
			s.writeOutput("%s\n", line)
			lastPrintedLine = lineno
		}
	}

	s.totalMatchCount += matchCount
	s.totalResultCount++
}
