# sift-pipe

[Sift](https://sift-tool.org/) is a fast and powerful open source alternative to grep.

Sift-pipe facilitates using sift in a pipe, expecting its configuration as JSON from the standard input.

### Configuration

The root JSON object has two attributes:
* Targets: []string
* Search:  []Options

Option is a JSON object with the following attributes:
* Pattern:         string
* ShowFilename:    bool
* ShowLineNumbers: bool
* ShowByteOffset:  bool
* Stats:           bool
* SkipBytes:       int
* Limit:           int

### Install with Working Go Environment

If you have a working go environment, you can install sift using "go get":

```go get github.com/kamphaus/sift-pipe```

## License

Copyright (C) 2018 Christophe Kamphaus

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, version 3 of the License.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.

