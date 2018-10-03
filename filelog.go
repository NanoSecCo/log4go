// Copyright (C) 2010, Kyle Lemons <kyle@kylelemons.net>.  All rights reserved.

package log4go

import (
	"bytes"
	"fmt"
	"os"
	"time"
	"strconv"
	"strings"
	"io/ioutil"
	"sort"
	"io"
	"path/filepath"
	//"reflect"
)

type timer struct {
	*time.Timer
	scr bool
}

// This log writer sends output to a file
type FileLogWriter struct {
	rec chan *LogRecord
	rot chan bool

	defaultFilename string

	// The opened file
	filename string
	file     *os.File
	suffixCounter int

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
	var nbuf, n int
	var window int
	offset = 0
	n = 0
	nbuf = 0
	w := &FileLogWriter{
		rec:       		  make(chan *LogRecord, LogBufferLength),
		rot:       		  make(chan bool),
		defaultFilename:  fname,
		filename: 		  fname,
		format:   		  "[%D %T] [%L] (%S) %M",
		rotate:   		  rotate,
		maxbackup:		  999,
		//buffer: 		    make([]byte, w.capacity),
		buff: 	  		  bytes.NewBuffer(make([]byte, 0, 8192)),
		log_var:  		  false, //default disabled
		capacity: 		  8192,
		timeout:  		  18000000000, //18sec timer flush
		position: 		  0,
	}

	// handle shutdown signals
	s := make(chan os.Signal, 1)
	notifySignals(s)

	// open the file for the first time
	if err = w.initializeNewFile(true); err != nil {
		fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
		return nil
	}

	// from here timeout
	t := NewTimer(time.Duration(w.timeout))

	go func() {

		defer func() {
			if w.file != nil {
				fmt.Fprint(w.file, FormatLogRecord(w.trailer, &LogRecord{Created: time.Now()}))
				w.file.Close()
			}
		}()

		for {
		    	select {
			case <-w.rot:
				if err = w.initializeNewFile(false); err != nil {
					fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
					return
				}
			case <-t.C:
				t.SCR()
				if (w.log_var == true && offset+w.position > 0) {
					// fmt.Println("received timeout signal <<<<")
					// fmt.Printf("file=%s, buff_content=%d\n", w.file, offset+w.position)
					n, err = fmt.Fprint(w.file, (string)(w.buff.String()))
					w.position =0
					w.buff.Reset()
				}
				// reset timer
				t.SafeReset(time.Duration(w.timeout))
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
				
				if (w.maxlines > 0 && w.maxlines_curlines >= w.maxlines) ||
					(w.maxsize > 0 && w.maxsize_cursize >= w.maxsize) {
					// flush buffer 
					n, err = fmt.Fprint(w.file, (string)(w.buff.String()))
					if err != nil {
						fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
						return
					}
					w.position =0
					w.buff.Reset()

					if err = w.initializeNewFile(false); err != nil {
						fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
						return
					}
				}
				if w.log_var == false {
					//fmt.Println("one(w) <----w.rec")
					n, err = fmt.Fprint(w.file, FormatLogRecord(w.format, rec))
				} else {
					// compute length of record 
					record_len := len(string(FormatLogRecord(w.format, rec)))
					// fmt.Println("rec=", rec)
					//////////////// trial code for buffer flexibility
					window = w.capacity - w.position
					if (window < w.capacity/2) {
						t.SafeReset(time.Duration(w.timeout)) // early
					}
						
					if(record_len < window) {
						// fmt.Printf("rec_len=%d, diff=%d\n", record_len, w.capacity-w.position)
						// write to buffer
						// update the accumulation
						offset, err = w.buff.WriteString(string(FormatLogRecord(w.format, rec)))
						if err != nil {
							fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
							return
						}
						w.position += offset 
						n = offset
				 	} else { // record can't fit and buffer is fullest to it capacity
						// fmt.Println("buff(w) <----w.rec")
						// elapsed := time.Since(now)
						// fmt.Printf("[<-]-- Buffer fill time %s", elapsed)
						nbuf, err = fmt.Fprint(w.file, (string)(w.buff.String()))
						w.position =0;
						w.buff.Reset()

						//handle additional record
						offset, err = w.buff.WriteString(string(FormatLogRecord(w.format, rec)))
						w.position += offset
						t.SafeReset(time.Duration(w.timeout)) // early
					}	
					//////////////////////end
/*
					//////////////////////old
					offset, err = w.buff.WriteString(string(FormatLogRecord(w.format, rec)))
					if err != nil {
						fmt.Fprintf(os.Stderr, "FileLogWriter(%q): %s\n", w.filename, err)
						return
					}

					w.position += offset 
					//fmt.Printf("file=%s, offset=%d, w.position=%d\n", w.file, offset, w.position)
					if (w.position > w.thresold) { 
					//if (offset > (w.capacity - w.position) 
						fmt.Println("buff(w) <----w.rec")
						elapsed := time.Since(now)
						fmt.Printf("[<-]-- Buffer fill time %s", elapsed)
						n, err = fmt.Fprint(w.file, (string)(w.buff.String()))
						w.position =0;
						w.buff.Reset()
						t.SafeReset(time.Duration(w.timeout)) // early
						offset, err = w.buff.WriteString(string(FormatLogRecord(w.format, rec)))
					}
					/////////////// old end  
*/
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
func (w *FileLogWriter) initializeNewFile(startup bool) error {

	// This function is called on startup of writer process 
	// and also when a file maxsize or maxlines is exceeded
	
	// Close any log file that may be open
	if w.file != nil {
		fmt.Fprint(w.file, FormatLogRecord(w.trailer, &LogRecord{Created: time.Now()}))
		w.file.Close()
	}

	if w.rotate{

		// For startup, if there are log files already present, append to latest file
		if startup{			

			// Read directory to see if files already exist
			dir := filepath.Dir(w.defaultFilename)
	
			files, err := ioutil.ReadDir(dir)
			if err != nil {
				return err
			}
		
			sort.Slice(files, func(i, j int) bool { 
				return files[i].ModTime().After(files[j].ModTime())
			})
		
			for _, v := range files {

				// If default file is the latest, then filename and suffix counter need not be updated
				if v.Name() == filepath.Base(w.defaultFilename){
					break
				}
				
				// Get latest file and update filename and current suffix
				if isLogFile(v, filepath.Base(w.defaultFilename)) {					
	
					w.filename = filepath.Join(dir, v.Name())

					extension := filepath.Ext(w.filename)	
					w.suffixCounter, err = strconv.Atoi(strings.TrimPrefix(extension, "."))
					if err != nil{
						return err
					}
	
					break
				}
			}
	
		} else {

			// If not startup, it means a file has exceeded its maxlines or maxsize
			// Hence start writing to next file
						
			w.suffixCounter ++
	
			var newFile string
			
			if w.suffixCounter <= w.maxbackup {
				newFile = w.defaultFilename + "." + strconv.Itoa(w.suffixCounter)
			} else {
				// If suffix counter reaches max backup limit, start overwriting initial file and repeat
				newFile = w.defaultFilename
				w.suffixCounter = 0
			}	
	
			os.Remove(newFile)
			w.filename = newFile			
	
			w.file.Close()	
		}
	}

	// Open the log file in read/write, append and create mode
	fd, err := os.OpenFile(w.filename, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0660)
	if err != nil {
		return err
	}

	w.file = fd

	now := time.Now()
	fmt.Fprint(w.file, FormatLogRecord(w.header, &LogRecord{Created: now}))

	// update maxlines and max size	
	if w.maxlines_curlines, err = getNumberOfLines(fd); err != nil {
		return err
	}

	stat, err := fd.Stat()
	if err != nil {
		return err
	}

	w.maxsize_cursize = int(stat.Size())

	return nil
}


func getNumberOfLines(r io.Reader) (int, error) {
    buf := make([]byte, 32*1024)
    count := 0
    lineSep := []byte{'\n'}

    for {
        c, err := r.Read(buf)
        count += bytes.Count(buf[:c], lineSep)

        switch {
        case err == io.EOF:
            return count, nil

        case err != nil:
            return count, err
        }
    }
}

func isLogFile(file os.FileInfo, logPrefix string) (logfile bool){

	if !file.IsDir() && strings.HasPrefix(file.Name(), logPrefix) && 
	   !strings.HasSuffix(file.Name(), ".status"){

		logfile = true
	}

	return
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

// Set/enable buffered logging (chainable). Must be called before the first log message is
// written.
func (w *FileLogWriter) SetBlog(blog bool) *FileLogWriter {
	//fmt.Fprintf(os.Stderr, "FileLogWriter.SetBlog: %v\n", daily)
	w.log_var = blog 
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

/*

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
    //_, err := fmt.Fprint(w.file, reflect.ValueOf(slv))
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
*/

func ToSlice(c chan interface{}) []interface{} {
    s := make([]interface{}, 0)
    for i := range c {
        s = append(s, i)
    }
    return s
}

//saw channel read, must be called after receiving value from timer chan
func (t *timer) SCR() {
	t.scr = true
}

func (t *timer) SafeReset(d time.Duration) bool {
	ret := t.Stop()
	if !ret && !t.scr {
		<-t.C
	}
	t.Timer.Reset(d)
	t.scr = false
	return ret
}

func NewTimer(d time.Duration) *timer {
	return &timer{
		Timer: time.NewTimer(d),
	}
}
