// Copyright (C) 2010, Kyle Lemons <kyle@kylelemons.net>.  All rights reserved.

package log4go

import (
	"bytes"
	"fmt"
	"os"
	"time"
	"reflect"
	"os/signal"
	"syscall"
)

// buffer filters
var n int
var w *FileLogWriter

// This log writer sends output to a file
type FileLogWriter struct {
	rec chan *LogRecord
	rot chan bool

	// The opened file
	filename string
	file     *os.File

	// The logging format
	format string

	// File header/trailer
	header, trailer string

	// Rotate at linecount
	maxlines          int
	maxlines_curlines int

	// Rotate at size
	maxsize         int
	maxsize_cursize int

	// Rotate daily
	daily          bool
	daily_opendate int

	// Keep old logfiles (.001, .002, etc)
	rotate    bool
	maxbackup int

	//buffer
	buffer []byte
	position int
	buff *bytes.Buffer

	// log buffering dis/enable 
	log_var bool

	// buffer capacity
	capacity int
	timeout int 
}

// This is the FileLogWriter's output method
func (w *FileLogWriter) LogWrite(rec *LogRecord) {
	w.rec <- rec
	//fmt.Printf("len=%d, cap=%d\n", len(w.rec), cap(w.rec))
}

func (w *FileLogWriter) Close() {
	close(w.rec)
	w.file.Sync()
}

// NewFileLogWriter creates a new LogWriter which writes to the given file and
// has rotation enabled if rotate is true.
//
// If rotate is true, any time a new log file is opened, the old one is renamed
// with a .### extension to preserve it.  The various Set* methods can be used
// to configure log rotation based on lines, size, and daily.
//
// The standard log-line format is:
//   [%D %T] [%L] (%S) %M
func NewFileLogWriter(fname string, rotate bool) *FileLogWriter {
	var err error
	var offset int 
	w = &FileLogWriter{
		rec:       make(chan *LogRecord, LogBufferLength),
		rot:       make(chan bool),
		filename:  fname,
		format:    "[%D %T] [%L] (%S) %M",
		rotate:    rotate,
		maxbackup: 999,
		//buffer:    make([]byte, w.capacity),
		buff: 	   bytes.NewBuffer(make([]byte, 0, 8192)),
		log_var:   true,
		capacity:  8192,
		timeout:   300000000000, 
	}

	// handle shutdown signals
	s := make(chan os.Signal, 1)
	signal.Notify(s,
		syscall.SIGQUIT,
		syscall.SIGKILL, 
		syscall.SIGINT, 
		syscall.SIGTERM,
		syscall.SIGTSTP)

	// open the file for the first time
	if err = w.intRotate(); err != nil {
		fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
		return nil
	}
	
	go func() {

		// from here timeout
		timeout := time.After(time.Duration(w.timeout))

		defer func() {
			if w.file != nil {
				fmt.Fprint(w.file, FormatLogRecord(w.trailer, &LogRecord{Created: time.Now()}))
				w.file.Close()
			}
		}()

		for {
		    	select {
			case <-w.rot:
				if err = w.intRotate(); err != nil {
					fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
					return
				}
			case <-timeout:
				if (w.log_var == true && offset+w.position > 0) {
					//fmt.Println("received timeout signal <<<<")
					//fmt.Printf("file=%s, buff_content=%d\n", w.file, offset+w.position)
					n, err = fmt.Fprint(w.file, (string)(w.buff.String()))
					w.position =0;
					w.buff.Reset()
				
					// re-arm timer
					timeout = time.After(time.Duration(w.timeout))
				}
			case <-s:
				fmt.Println("received shutdown signals <<<<")
				if w.log_var == true {
					fmt.Printf("file=%s, buff_content=%d\n", w.file, offset+w.position)
					n, err = fmt.Fprint(w.file, (string)(w.buff.String()))
					w.position =0;
					w.buff.Reset()
					os.Exit(1)
				} else {
					fmt.Println("Log buff disabled, no action\n")
					os.Exit(1)
				}
			case rec, ok := <-w.rec:
				if !ok {
					return
				}
				now := time.Now()
				if (w.maxlines > 0 && w.maxlines_curlines >= w.maxlines) ||
					(w.maxsize > 0 && w.maxsize_cursize >= w.maxsize) ||
					(w.daily && now.Day() != w.daily_opendate) {
					if err = w.intRotate(); err != nil {
						fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
						return
					}
				}
				if w.log_var == false {
					//fmt.Println("one(w) <----w.rec")
					n, err = fmt.Fprint(w.file, FormatLogRecord(w.format, rec))
				} else {
					//fmt.Println("rec=", rec)
					offset, err = w.buff.WriteString(string(FormatLogRecord(w.format, rec)))
					if err != nil {
						fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
						return
					}

					w.position += offset 
					//fmt.Printf("file=%s, offset=%d, w.position=%d\n", w.file, offset, w.position)
					if(offset + w.position) > w.capacity {
						//fmt.Println("buff(w) <----w.rec")
						n, err = fmt.Fprint(w.file, (string)(w.buff.String()))
						w.position =0;
						w.buff.Reset()
					}  
				} // log buffering enabled
					
				// Update the counts
				w.maxlines_curlines++
				w.maxsize_cursize += n 
				//fmt.Printf("lines=%d, size=%d\n", w.maxlines_curlines, w.maxsize_cursize)
			}
		}
	}()
	return w
}

// Request that the logs rotate
func (w *FileLogWriter) Rotate() {
	w.rot <- true
}

// If this is called in a threaded context, it MUST be synchronized
func (w *FileLogWriter) intRotate() error {
	// Close any log file that may be open
	if w.file != nil {
		fmt.Fprint(w.file, FormatLogRecord(w.trailer, &LogRecord{Created: time.Now()}))
		w.file.Close()
	}

	// If we are keeping log files, move it to the next available number
	if w.rotate {
		_, err := os.Lstat(w.filename)
		if err == nil { // file exists
			// Find the next available number
			num := 1
			fname := ""
			if w.daily && time.Now().Day() != w.daily_opendate {
				yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

				for ; err == nil && num <= 999; num++ {
					fname = w.filename + fmt.Sprintf(".%s.%03d", yesterday, num)
					_, err = os.Lstat(fname)
				}
				// return error if the last file checked still existed
				if err == nil {
					return fmt.Errorf("Rotate: Cannot find free log number to rename %s\n", w.filename)
				}
			} else {
				num = w.maxbackup - 1
				for ; num >= 1; num-- {
					fname = w.filename + fmt.Sprintf(".%d", num)
					nfname := w.filename + fmt.Sprintf(".%d", num+1)
					_, err = os.Lstat(fname)
					if err == nil {
						os.Rename(fname, nfname)
					}
				}
			}

			w.file.Close()
			// Rename the file to its newfound home
			err = os.Rename(w.filename, fname)
			if err != nil {
				return fmt.Errorf("Rotate: %s\n", err)
			}
		}
	}

	// Open the log file
	fd, err := os.OpenFile(w.filename, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0660)
	if err != nil {
		return err
	}
	w.file = fd

	now := time.Now()
	fmt.Fprint(w.file, FormatLogRecord(w.header, &LogRecord{Created: now}))

	// Set the daily open date to the current date
	w.daily_opendate = now.Day()

	// initialize rotation values
	w.maxlines_curlines = 0
	w.maxsize_cursize = 0

	return nil
}

func (w *FileLogWriter) SetTimeout(timeout int) *FileLogWriter {
	w.timeout = timeout
	return w
}

func (w *FileLogWriter) SetCapacity(capacity int) *FileLogWriter {
	w.capacity = capacity
	return w
}

// Set the logging format (chainable).  Must be called before the first log
// message is written.
func (w *FileLogWriter) SetFormat(format string) *FileLogWriter {
	w.format = format
	return w
}

// Set the logfile header and footer (chainable).  Must be called before the first log
// message is written.  These are formatted similar to the FormatLogRecord (e.g.
// you can use %D and %T in your header/footer for date and time).
func (w *FileLogWriter) SetHeadFoot(head, foot string) *FileLogWriter {
	w.header, w.trailer = head, foot
	if w.maxlines_curlines == 0 {
		fmt.Fprint(w.file, FormatLogRecord(w.header, &LogRecord{Created: time.Now()}))
	}
	return w
}

// Set rotate at linecount (chainable). Must be called before the first log
// message is written.
func (w *FileLogWriter) SetRotateLines(maxlines int) *FileLogWriter {
	//fmt.Fprintf(os.Stderr, "FileLogWriter.SetRotateLines: %v\n", maxlines)
	w.maxlines = maxlines
	return w
}

// Set rotate at size (chainable). Must be called before the first log message
// is written.
func (w *FileLogWriter) SetRotateSize(maxsize int) *FileLogWriter {
	//fmt.Fprintf(os.Stderr, "FileLogWriter.SetRotateSize: %v\n", maxsize)
	w.maxsize = maxsize
	return w
}

// Set rotate daily (chainable). Must be called before the first log message is
// written.
func (w *FileLogWriter) SetRotateDaily(daily bool) *FileLogWriter {
	//fmt.Fprintf(os.Stderr, "FileLogWriter.SetRotateDaily: %v\n", daily)
	w.daily = daily
	return w
}

// Set max backup files. Must be called before the first log message
// is written.
func (w *FileLogWriter) SetRotateMaxBackup(maxbackup int) *FileLogWriter {
	w.maxbackup = maxbackup
	return w
}

// SetRotate changes whether or not the old logs are kept. (chainable) Must be
// called before the first log message is written.  If rotate is false, the
// files are overwritten; otherwise, they are rotated to another file before the
// new log is opened.
func (w *FileLogWriter) SetRotate(rotate bool) *FileLogWriter {
	//fmt.Fprintf(os.Stderr, "FileLogWriter.SetRotate: %v\n", rotate)
	w.rotate = rotate
	return w
}

// NewXMLLogWriter is a utility method for creating a FileLogWriter set up to
// output XML record log messages instead of line-based ones.
func NewXMLLogWriter(fname string, rotate bool) *FileLogWriter {
	return NewFileLogWriter(fname, rotate).SetFormat(
		`	<record level="%L">
		<timestamp>%D %T</timestamp>
		<source>%S</source>
		<message>%M</message>
	</record>`).SetHeadFoot("<log created=\"%D %T\">", "</log>")
}

func ChanToSlice(ch interface{}) interface{} {
    //var buffer bytes.Buffer
    fmt.Println("....in ChanToSlice")
    chv := reflect.ValueOf(ch)
    slv := reflect.MakeSlice(reflect.SliceOf(reflect.TypeOf(ch).Elem()), 0, 0)
    //fmt.Println("slv=", slv.Interface())
    for i:=0; i<4; i++ {
    //for {
        v, ok := chv.Recv()
    	//copy(w.buffer[w.position:], reflect.ValueOf(v).String())
    	//copy(w.buffer[w.position:], v.[]String())
    	//w.position += v.Len() 
	//n, err = fmt.Fprint(w.file, FormatLogRecord(w.format, sl))
    	fmt.Println("v=", v)
        if !ok {
            return slv.Interface()
        }
        slv = reflect.Append(slv, v)
    }
    //buff.Write(reflect.ValueOf(slv).Elem())
    //_, err := fmt.Fprint(w.file, FormatLogRecord(w.format, slv.Interface().(*LogRecord)))
    fmt.Println("write slv slice\n")
    //_, err := fmt.Fprint(w.file, slv.Interface().(*LogRecord))
    _, err := fmt.Fprint(w.file, reflect.ValueOf(slv))
    if err != nil {
	fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
    }
    fmt.Println("after - write slv slice\n")
    //buffer.Write(log)
    //copy(w.buffer[w.position:], reflect.ValueOf(slv).(Elem))
    //w.position += len(slv) 
    //fmt.Println("2....after for loop", slv.Interface())
    return slv
}

func ToSlice(c chan interface{}) []interface{} {
    s := make([]interface{}, 0)
    for i := range c {
        s = append(s, i)
    }
    return s
}
