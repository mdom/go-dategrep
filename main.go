package main

import (
	"bufio"
	"compress/bzip2"
	"compress/gzip"
	"flag"
	"fmt"
	"github.com/mdom/dtgrep/dateflag"
	"github.com/mdom/dtgrep/fixtime"
	"github.com/mdom/dtgrep/retime"
	"io"
	"log"
	"os"
	"path"
	"sort"
	"time"
)

var now = time.Now()
var epoch time.Time
var loc = time.Local

var Version = "unknown"
var CommitHash = "unknown"
var BuildDate = "unknown"

type Options struct {
	from, to     time.Time
	skipDateless bool
	multiline    bool
}

type Iterator struct {
	filename string
	reader   io.Reader
	*bufio.Scanner
	Line string
	Time time.Time
	Err  error
}

type Iterators []*Iterator

func (it Iterators) Len() int           { return len(it) }
func (it Iterators) Swap(i, j int)      { it[i], it[j] = it[j], it[i] }
func (it Iterators) Less(i, j int) bool { return it[i].Time.Before(it[j].Time) }

func inTimeRange(s *Iterator, from, to time.Time) bool {
	dt := s.Time
	return (dt.Equal(from) || dt.After(from)) && dt.Before(to)
}

func filter(s Iterators, from, to time.Time) Iterators {
	var p Iterators
	for _, v := range s {
		if v.Err == nil && inTimeRange(v, from, to) {
			p = append(p, v)
		}
	}
	return p
}

var formats = map[string]string{
	"rsyslog": "Jan _2 15:04:05",
	"rfc3339": time.RFC3339,
	"apache":  "02/Jan/2006:15:04:05 -0700",
}

func dateRange(from, to time.Time, duration time.Duration) (time.Time, time.Time) {

	// --duration, --from and --to specified
	if duration != 0 && !to.IsZero() && !from.IsZero() {
		log.Fatalln("--duration can only be used with either --from or --to.")
	}

	// only --duration specified
	if duration != 0 && to.IsZero() && from.IsZero() {
		switch {
		case duration.Hours() >= 1:
			to = now.Truncate(time.Duration(1) * time.Hour)
		case duration.Minutes() >= 1:
			to = now.Truncate(time.Duration(1) * time.Minute)
		default:
			to = now.Truncate(time.Duration(1) * time.Second)
		}
		from = to.Add(-duration)
	}

	if duration != 0 && !to.IsZero() && from.IsZero() {
		from = to.Add(-duration)
	}

	if duration != 0 && to.IsZero() && !from.IsZero() {
		to = from.Add(duration)
	}

	if to.IsZero() {
		to = now
	}

	return from, to
}

func main() {

	log.SetFlags(0)
	log.SetPrefix("")

	var formatName, location string

	toFlag := dateflag.DateFlag{Now: now}
	fromFlag := dateflag.DateFlag{Now: now}

	var duration time.Duration

	var options Options

	defaultFormat := "rsyslog"
	if os.Getenv("GO_DATEGREP_FORMAT") != "" {
		defaultFormat = os.Getenv("GO_DATEGREP_FORMAT")
	}

	flag.Var(&fromFlag, "from", "Print all lines from `DATESPEC` inclusively.")
	flag.Var(&toFlag, "to", "Print all lines until `DATESPEC` exclusively.")

	flag.StringVar(&formatName, "format", defaultFormat, "Use `FORMAT` to parse file.")
	flag.BoolVar(&options.skipDateless, "skip-dateless", false, "Ignore all lines without timestamp.")
	flag.BoolVar(&options.multiline, "multiline", false, "Print all lines between the start and end line even if they are not timestamped.")
	flag.StringVar(&location, "location", time.Local.String(), "Use location in the absence of any timezone information.")

	flag.DurationVar(&duration, "duration", 0, "Print all lines in `DURATION` from --from or --to.")

	var displayVersion bool
	flag.BoolVar(&displayVersion, "version", false, "Display version")

	flag.Lookup("to").DefValue = "now"
	flag.Lookup("from").DefValue = "epoch"

	flag.Parse()

	if displayVersion {
		log.Printf("version: %s\ncommit: %s\nbuild date: %s\n",
			Version, CommitHash, BuildDate)
		return
	}

	var err error

	loc, err = time.LoadLocation(location)
	if err != nil {
		log.Fatalln("Can't load location:", err)
	}

	options.from, options.to = dateRange(fromFlag.Get(), toFlag.Get(), duration)

	if options.from.After(options.to) || options.from.Equal(options.to) {
		log.Fatalln("Start date must be before end date.")
	}

	var format retime.Format
	for name, template := range formats {
		if name == formatName {
			format, err = retime.New(template, loc)
			if err != nil {
				log.Fatalln("Can't create format:", err)
			}
			break
		}
	}

	if (format == retime.Format{}) {
		format, err = retime.New(formatName, loc)
		if err != nil {
			log.Fatalln("Can't create format:", err)
		}
	}

	var iterators = make(Iterators, 0)

	if len(flag.Args()) > 0 {
		for _, filename := range flag.Args() {

			if filename == "-" {
				i := &Iterator{filename: filename, reader: os.Stdin, Scanner: bufio.NewScanner(os.Stdin)}
				iterators = append(iterators, i)
				continue
			}

			file, err := os.Open(filename)
			if err != nil {
				log.Fatalln("Cannot open", filename, ":", err)
			}
			defer file.Close()

			// mimeType support?
			ext := path.Ext(filename)
			if ext == ".gz" || ext == ".z" {
				r, err := gzip.NewReader(file)
				defer r.Close()
				if err != nil {
					log.Fatalln("Cannot open", filename, ":", err)
				}
				i := &Iterator{filename: filename, reader: r, Scanner: bufio.NewScanner(r)}
				iterators = append(iterators, i)
			} else if ext == ".bz2" || ext == ".bz" {
				r := bzip2.NewReader(file)
				i := &Iterator{filename: filename, reader: r, Scanner: bufio.NewScanner(r)}
				iterators = append(iterators, i)
			} else {
				scanner, err := findStartSeekable(file, options, format)
				switch {
				case err == io.EOF:
					// daterange not in file, skip
					continue
				case err != nil:
					log.Fatalln("Error finding dates in ", filename, ":", err)
				}
				i := &Iterator{filename: filename, reader: file, Scanner: scanner}
				iterators = append(iterators, i)
			}
		}
	} else {
		i := &Iterator{filename: "-", reader: os.Stdin, Scanner: bufio.NewScanner(os.Stdin)}
		iterators = append(iterators, i)
	}

	var ignoreError = options.skipDateless || options.multiline
	for _, i := range iterators {
		i.Scan(options.from, options.to, ignoreError, format)
	}

	for {

		iterators = filter(iterators, options.from, options.to)
		sort.Sort(iterators)

		if len(iterators) > 0 {
			var until time.Time
			if len(iterators) > 1 {
				until = iterators[1].Time
			} else {
				until = options.to
			}
			i := iterators[0]
			fmt.Println(i.Line)
			i.Print(until, options, format)
		} else {
			break
		}
	}
}

func (i *Iterator) Print(to time.Time, options Options, format retime.Format) {
	for {
		i.Line, i.Err = readline(i.Scanner)
		if i.Err == io.EOF {
			return
		}
		if i.Err != nil {
			// what file?
			log.Fatalln("Error reading file:", i.Err)
		}
		i.Time, i.Err = format.Extract(i.Line)
		i.Time = fixtime.AddYear(i.Time, now)

		switch {
		case i.Err != nil && options.multiline:
			fmt.Println(i.Line)
		case i.Err != nil && options.skipDateless:
			continue
		case i.Err != nil:
			log.Fatalln("Aborting. Found line without date:", i.Line)
		case i.Time.Before(to):
			fmt.Println(i.Line)
		default:
			return
		}
	}
}

func readline(s *bufio.Scanner) (string, error) {
	ret := s.Scan()
	if !ret && s.Err() == nil {
		return "", io.EOF
	}
	if !ret {
		return "", s.Err()
	}
	return s.Text(), nil
}

func (i *Iterator) Scan(from, to time.Time, ignoreError bool, format retime.Format) {
	for {
		i.Line, i.Err = readline(i.Scanner)
		if i.Err != nil {
			break
		}
		i.Time, i.Err = format.Extract(i.Line)
		i.Time = fixtime.AddYear(i.Time, now)
		if i.Err != nil && ignoreError {
			continue
		}
		if i.Err != nil {
			log.Fatalln("Aborting. Found line without date:", i.Line)
		}
		if i.Time.After(to) {
			i.Err = io.EOF
			break
		}
		if i.Time.Equal(from) || i.Time.After(from) {
			break
		}
	}
}

func findStartSeekable(f *os.File, options Options, format retime.Format) (*bufio.Scanner, error) {

	// find block size
	blockSize := int64(4096)

	fileInfo, err := f.Stat()
	if err != nil {
		return &bufio.Scanner{}, err
	}
	size := fileInfo.Size()
	min := int64(0)
	max := size / blockSize
	var mid int64

	var ignoreErrors = options.skipDateless || options.multiline

	for max-min > 1 {
		mid = (max + min) / 2
		f.Seek(mid*blockSize, os.SEEK_SET)
		scanner := bufio.NewScanner(f)

		_, err := readline(scanner) // skip partial line
		if err != nil {
			return scanner, err
		}

		var dt time.Time

		for {
			line, err := readline(scanner)
			if err != nil {
				return scanner, err
			}

			dt, err = format.Extract(line)
			dt = fixtime.AddYear(dt, now)
			if err != nil && ignoreErrors {
				continue
			}
			if err != nil {
				log.Fatalln("Aborting. Found line without date:", line)
			}
			break
			// optimization: while searching next line we entered next block
		}

		if dt.Before(options.from) {
			min = mid
		} else {
			max = mid
		}
	}

	min = min * blockSize
	_, err = f.Seek(min, os.SEEK_SET)
	if err != nil {
		return &bufio.Scanner{}, err
	}
	scanner := bufio.NewScanner(f)
	if min > 0 {
		_, err := readline(scanner) // skip partial line
		if err != nil {
			return scanner, err
		}
	}

	return scanner, nil
}
