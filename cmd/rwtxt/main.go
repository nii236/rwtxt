package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"flag"
	"fmt"
	"github.com/BurntSushi/toml"
	"io"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime/pprof"
	"strings"
	"time"

	"github.com/schollz/rwtxt/pkg/utils"

	log "github.com/cihub/seelog"
	_ "github.com/mattn/go-sqlite3"
	"github.com/schollz/rwtxt"
	"github.com/schollz/rwtxt/pkg/db"
)

var (
	dbName  string
	Version string
)

func main() {
	var (
		err           error
		debug         = flag.Bool("debug", false, "debug mode")
		showVersion   = flag.Bool("v", false, "show version")
		profileMemory = flag.Bool("memprofile", false, "profile memory")
		database      = flag.String("db", "rwtxt.db", "name of the database")
		dump          = flag.Bool("dump", false, "dump DB on start")
		listen        = flag.String("listen", rwtxt.DefaultBind, "interface:port to listen on")
		private       = flag.Bool("private", false, "private setup (allows listing of public notes)")
		importfolder  = flag.String("importfolder", "", "recursively import folder containing markdown files")
	)
	flag.Parse()

	if *profileMemory {
		go func() {
			for {
				time.Sleep(30 * time.Second)
				log.Info("writing memprofile")
				f, err := os.Create("memprofile")
				if err != nil {
					panic(err)
				}
				pprof.WriteHeapProfile(f)
				f.Close()
			}
		}()
	}

	if *showVersion {
		fmt.Println(Version)
		return
	}
	if *debug {
		err = setLogLevel("debug")
		db.SetLogLevel("debug")
	} else {
		err = setLogLevel("info")
		db.SetLogLevel("info")
	}
	if err != nil {
		panic(err)
	}
	dbName = *database
	defer log.Flush()

	fs, err := db.New(dbName, *dump)
	if err != nil {
		panic(err)
	}

	if *importfolder != "" {
		subdirs, err := ioutil.ReadDir(*importfolder)
		if err != nil {
			panic(err)
		}

		for _, subdir := range subdirs {
			if !subdir.IsDir() {
				continue
			}
			files, err := ioutil.ReadDir(filepath.Join(*importfolder, subdir.Name()))
			if err != nil {
				panic(err)
			}
			for _, file := range files {
				if !strings.Contains(file.Name(), ".md") {
					continue
				}
				b, err := ioutil.ReadFile(path.Join(*importfolder, subdir.Name(), file.Name()))
				if err != nil {
					panic(err)
				}
				err = importMarkdown(fs, "travel", file.Name(), string(b))
				if err != nil {
					panic(err)
				}
			}

		}
		return
	}

	config := rwtxt.Config{Private: *private}

	rwt, err := rwtxt.New(fs, config)
	if err != nil {
		panic(err)
	}
	if listen != nil && *listen != "" {
		rwt.Bind = *listen
	}

	err = rwt.Serve(*dump)
	if err != nil {
		log.Error(err)
	}
}

func findLinks(content string) []string {
	r := regexp.MustCompile(`!\[.*?\]\((.*?jpg|jpeg).*?\)`)
	matches := r.FindAllStringSubmatch(content, -1)

	result := []string{}
	for _, m := range matches {
		if strings.Contains(m[1], "http") {
			continue
		}
		result = append(result, m[1])
	}
	return result
}

type Frontmatter struct {
	Title       string    `toml:"title"`
	Description string    `toml:"description"`
	Date        time.Time `toml:"date"`
	Tags        []string  `toml:"tags"`
}

func replaceFrontmatter(content string, fm *Frontmatter) string {
	r := regexp.MustCompile(`(?s)\+\+\+(.*)\+\+\+`)
	content = r.ReplaceAllString(content, "")
	result := ""
	if fm.Title != "" {
		title := fmt.Sprintf("# %s", fm.Title)
		result += title
		result += "\n\n"
	}
	if fm.Description != "" {
		description := fmt.Sprintf("*%s*", fm.Description)
		result += description
		result += "\n\n"
	}

	result += content
	result += "\n\n"

	if len(fm.Tags) > 0 {
		tags := fmt.Sprintf("*%s*", strings.Join(fm.Tags, ","))
		result += tags
		result += "\n\n"

	}
	return result
}

func processFrontmatter(content string) (*Frontmatter, error) {
	r := regexp.MustCompile(`(?s)\+\+\+(.*)\+\+\+`)
	found := r.FindStringSubmatch(content)[1]
	result := &Frontmatter{}
	err := toml.Unmarshal([]byte(found), result)
	if err != nil {
		return nil, err
	}
	return result, nil

}
func processLink(fs *db.FileSystem, path string) (string, error) {
	// upload here and return new link
	h := sha256.New()
	basePath := "/home/nii236/git/travel-blog/static/"
	fullPath := filepath.Join(basePath, path)
	log.Debug(fullPath)
	f, err := os.Open(fullPath)
	if err != nil {
		return "", err
	}
	fname := f.Name()
	err = f.Close()
	if err != nil {
		return "", err
	}
	b, err := ioutil.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	_, err = h.Write(b)
	if err != nil {
		return "", err
	}
	if err != nil {
		return "", err
	}
	id := fmt.Sprintf("sha256-%x", h.Sum(nil))
	var fileData bytes.Buffer
	gw := gzip.NewWriter(&fileData)
	if err != nil {
		return "", err
	}

	_, err = io.Copy(gw, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	err = gw.Flush()
	if err != nil {
		return "", err
	}
	err = fs.SaveBlob(id, fname, fileData.Bytes())
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("/uploads/%s", id), nil
}

func updateMarkdown(content, oldLink, newLink string) string {
	return strings.Replace(content, oldLink, newLink, -1)
}

func processMarkdown(fs *db.FileSystem, content string) string {
	fm, err := processFrontmatter(content)
	if err != nil {
		log.Warn("cannot process frontmatter: ", err)
	}
	log.Debug(fm.Title)
	log.Debug(fm.Date)
	log.Debug(fm.Tags)
	links := findLinks(content)
	for _, oldLink := range links {
		newLink, err := processLink(fs, oldLink)
		if err != nil {
			log.Warn("oldLink not processed ", oldLink, err)
			continue
		}
		content = updateMarkdown(content, oldLink, newLink)
	}

	content = replaceFrontmatter(content, fm)
	return content
}
func importMarkdown(fs *db.FileSystem, domain, filename, content string) error {

	data := strings.TrimSpace(content)
	if data == rwtxt.IntroText {
		data = ""
	}
	fm, err := processFrontmatter(data)
	if err != nil {
		return err
	}
	data = processMarkdown(fs, data)

	slug := fmt.Sprintf("%s-%s", fm.Date.Format("2006-01-02"), filename)
	editFile := db.File{
		ID:      utils.UUID(),
		Slug:    slug,
		Data:    fmt.Sprintf("*%s*\n\n%s", slug, data),
		Created: time.Now(),
		Domain:  domain,
	}

	err = fs.Save(editFile)
	if err != nil {
		log.Warn(err)
		log.Warn("creating domain...temporary password 123")
		err = fs.SetDomain("travel", "123")
		if err != nil {
			log.Error(err)
		}
		err = fs.Save(editFile)
		if err != nil {
			return err
		}
	}

	return nil
}

// setLogLevel determines the log level
func setLogLevel(level string) (err error) {

	// https://en.wikipedia.org/wiki/ANSI_escape_code#3/4_bit
	// https://github.com/cihub/seelog/wiki/Log-levels
	appConfig := `
	<seelog minlevel="` + level + `">
	<outputs formatid="stdout">
	<filter levels="debug,trace">
		<console formatid="debug"/>
	</filter>
	<filter levels="info">
		<console formatid="info"/>
	</filter>
	<filter levels="critical,error">
		<console formatid="error"/>
	</filter>
	<filter levels="warn">
		<console formatid="warn"/>
	</filter>
	</outputs>
	<formats>
		<format id="stdout"   format="%Date %Time [%LEVEL] %File %FuncShort:%Line %Msg %n" />
		<format id="debug"   format="%Date %Time %EscM(37)[%LEVEL]%EscM(0) %File %FuncShort:%Line %Msg %n" />
		<format id="info"    format="%Date %Time %EscM(36)[%LEVEL]%EscM(0) %File %FuncShort:%Line %Msg %n" />
		<format id="warn"    format="%Date %Time %EscM(33)[%LEVEL]%EscM(0) %File %FuncShort:%Line %Msg %n" />
		<format id="error"   format="%Date %Time %EscM(31)[%LEVEL]%EscM(0) %File %FuncShort:%Line %Msg %n" />
	</formats>
	</seelog>
	`
	logger, err := log.LoggerFromConfigAsBytes([]byte(appConfig))
	if err != nil {
		return
	}
	log.ReplaceLogger(logger)
	return
}
