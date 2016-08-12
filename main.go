package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"time"
)

var now = time.Now()
var epoch time.Time

type Format struct {
	regexp   string
	name     string
	template string
}

type Options struct {
	from, to     time.Time
	skipDateless bool
	multiline    bool
}

var formats = []Format{
	{
		regexp:   `^[A-Z][a-z]{2} \d{2} \d{2}:\d{2}:\d{2}`,
		name:     "rsyslog",
		template: "Jan 02 15:04:05",
	},
}

func parse_date(date string, template string) (time.Time, error) {
	if date == "now" {
		return time.Now(), nil
	}
	dt, err := time.ParseInLocation(template, date, time.Local)
	if err != nil {
		return dt, err
	}
	now := time.Now()
	if dt.Year() == 0 {
		dt = dt.AddDate(now.Year(), 0, 0)
	}
	return dt, nil
}

func main() {

	log.SetFlags(0)
	log.SetPrefix("")

	var from_arg, to_arg, formatName string

	var options Options

	flag.StringVar(&from_arg, "from", epoch.Format(time.RFC3339), "Print all lines from `DATESPEC` inclusively.")
	flag.StringVar(&to_arg, "to", "now", "Print all lines until `DATESPEC` exclusively.")
	flag.StringVar(&formatName, "format", "rsyslog", "Use `Format` to parse file.")
	flag.BoolVar(&options.skipDateless, "skip-dateless", false, "Ignore all lines without timestamp.")
	flag.BoolVar(&options.multiline, "multiline", false, "Print all lines between the start and end line even if they are not timestamped.")

	flag.Parse()

	var err error
	options.from, err = parse_date(from_arg, time.RFC3339)
	if err != nil {
		log.Fatalln("Can't parse --from:", err)
	}
	options.to, err = parse_date(to_arg, time.RFC3339)
	if err != nil {
		log.Fatalln("Can't parse --to:", err)
	}

	var format Format
	for _, f := range formats {
		if f.name == formatName {
			format = f
			break
		}
	}

	if (format == Format{}) {
		log.Fatalln("Unknown format:", formatName)
	}

	var files = make([]*os.File, 0)

	if len(flag.Args()) > 0 {
		for _, filename := range flag.Args() {

			if filename == "-" {
				files = append(files, os.Stdin)
				continue
			}

			file, err := os.Open(filename)
			if err != nil {
				log.Fatalln("Cannot open", filename, ":", err)
			}
			defer file.Close()

			offset, err := findOffset(file, options, format)
			switch {
			case err == io.EOF:
				// daterange not in file, skip
				continue
			case err != nil:
				log.Fatalln("Error finding dates in ", filename, ":", err)
			}
			_, err = file.Seek(offset, os.SEEK_SET)
			if err != nil {
				log.Fatalln("Can't seek ", filename, ":", err)
			}
			files = append(files, file)
		}
	} else {
		files = append(files, os.Stdin)
	}

	for _, file := range files {

		scanner := bufio.NewScanner(file)
		for {
			line, err := nextLine(scanner)
			if err == io.EOF {
				break
			}
			if err != nil {
				// what file?
				log.Fatalln("Error reading file:", err)
			}
			dt, err := getLineTime(line, format)

			switch {
			case err != nil && options.multiline:
				fmt.Println(line)
			case err != nil && options.skipDateless:
				continue
			case err != nil:
				log.Fatalln("Aborting. Found line without date:", line)
			case dt.Before(options.to):
				fmt.Println(line)
			default:
				break
			}
		}
	}
}

func getLineTime(line string, format Format) (time.Time, error) {
	re := regexp.MustCompile(format.regexp)
	time_string := re.FindString(line)
	dt, err := parse_date(time_string, format.template)
	return dt, err
}

func nextLine(s *bufio.Scanner) (string, error) {
	ret := s.Scan()
	if !ret && s.Err() == nil {
		return "", io.EOF
	}
	if !ret {
		return "", s.Err()
	}
	return s.Text(), nil
}

func findOffset(f *os.File, options Options, format Format) (offset int64, err error) {
	// find block size
	block_size := int64(4096)

	file_info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := file_info.Size()
	min := int64(0)
	max := size / block_size
	var mid int64

	var ignore_errors = options.skipDateless || options.multiline

	for max-min > 1 {
		mid = (max + min) / 2
		f.Seek(mid*block_size, os.SEEK_SET)
		scanner := bufio.NewScanner(f)

		_, err := nextLine(scanner) // skip partial line
		if err != nil {
			return 0, err
		}

		var dt time.Time

		for {
			line, err := nextLine(scanner)
			if err != nil {
				return 0, err
			}

			dt, err = getLineTime(line, format)
			if err != nil && ignore_errors {
				continue
			}
			if err != nil {
				log.Fatalln("Aborting. Found line without date:", line)
			}
			break
		}

		if dt.Before(options.from) {
			min = mid
		} else {
			max = mid
		}
	}

	min = min * block_size
	offset = min
	f.Seek(min, os.SEEK_SET)
	scanner := bufio.NewScanner(f)

	line, err := nextLine(scanner) // skip partial line
	if err != nil {
		return 0, err
	}
	offset += int64(len(line) + 1)

	for {
		line, err := nextLine(scanner)
		if err != nil {
			return 0, err
		}
		dt, err := getLineTime(line, format)
		if err != nil && ignore_errors {
			continue
		}
		if err != nil {
			log.Fatalln("Aborting. Found line without date:", line)
		}
		if dt.After(options.from) {
			return offset, nil
		}
		offset += int64(len(line) + 1)
	}
	return 0, io.EOF
}
