package main

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ktrysmt/go-bitbucket"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
	"gopkg.in/src-d/go-git.v4/plumbing/transport/http"
	yaml "gopkg.in/yaml.v2"
)

var (
	checkFileName  = "check.txt"
	configFileName = "config.yml"
	resultDir      = "result"
	cleanPaths     []string
	start          = time.Now()
)

// Config structure for script
type Config struct {
	Username string `yaml:"user"`
	Passwd   string `yaml:"passwd"`
	Owner    string `yaml:"owner"`
	Clone    string `yaml:"clone"`
	Pattern  string `yaml:"pattern"`
}

// parse the config file
func parseYamlFile(filename string, conf *Config) error {
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, conf)
}

// retrieve all the repositories by the owner and role
func retrieveRepositories(c *Config) (map[string]string, error) {
	client := bitbucket.NewBasicAuth(c.Username, c.Passwd)
	opt := &bitbucket.RepositoriesOptions{
		Owner: c.Owner,
		Role:  "owner",
	}

	res, err := client.Repositories.ListForAccount(opt)
	if err != nil {
		return nil, err
	}

	repositories := map[string]string{}
	for _, item := range res.Items {
		for _, link := range item.Links["clone"].([]interface{}) {
			// map[href:https://wesure@bitbucket.org/wesure/alert-enricher.git name:https]
			// map[href:git@bitbucket.org:wesure/alert-enricher.git name:ssh]
			format := fmt.Sprintf("%v", link)
			if strings.Contains(format, "https") {
				head := strings.Split(format, " ")[0]
				val := head[strings.Index(head, ":")+1:]

				repos := val[strings.LastIndex(val, "/")+1:]
				key := strings.Split(repos, ".")[0]

				repositories[key] = val
				break
			}
		}
	}
	return repositories, nil
}

// check if the config files in repository is changed
func checkRepository(c *Config, repos string, url string, lastTime time.Time, fd *os.File) error {
	dest := filepath.Join(c.Clone, repos)

	// clone the repository
	repository, err := git.PlainClone(dest, false, &git.CloneOptions{
		URL:      url,
		Progress: os.Stdout,
		Auth: &http.BasicAuth{
			Username: c.Username,
			Password: c.Passwd,
		},
	})
	if err != nil {
		return err
	}
	// record the local repository path
	cleanPaths = append(cleanPaths, dest)

	// retrieving the branch by HEAD
	ref, err := repository.Head()
	if err != nil {
		return err
	}

	// retrieving the commit object
	commit, err := repository.CommitObject(ref.Hash())
	if err != nil {
		return err
	}

	// retrieve the tree from the commit and file list
	tree, err := commit.Tree()
	if err != nil {
		return err
	}

	result := map[string]time.Time{}
	tree.Files().ForEach(func(f *object.File) error {
		// only check the config files
		if strings.Contains(f.Name, c.Pattern) {
			// retrieve the commit log for the config files
			iter, err := repository.Log(&git.LogOptions{
				From:     ref.Hash(),
				Order:    git.LogOrderCommitterTime,
				FileName: &f.Name,
			})
			if err != nil {
				return err
			}

			// check if the config file is changed
			iter.ForEach(func(c *object.Commit) error {
				// pkg/misc/conf/business.yml in repository(alert-enricher) has changed in time(Tue Dec 10 13:07:28 2019 +0800)
				if c.Author.When.Sub(lastTime) > 0 {
					t, ok := result[f.Name]
					if !ok {
						result[f.Name] = c.Author.When
					} else {
						if c.Author.When.Sub(t) > 0 {
							result[f.Name] = c.Author.When
						}
					}
				}
				return nil
			})
		}

		return nil
	})

	for key, val := range result {
		line := fmt.Sprintf("%s in repository(%s) has changed in latest time(%s)\n", key, repos, val.Format(object.DateFormat))
		if _, err := fd.WriteString(line); err != nil {
			return err
		}
	}
	if len(result) > 0 {
		fd.WriteString("\n")
	}

	return nil
}

func readCheckTime(filename string) (time.Time, error) {
	// check if the file is existed
	_, err := os.Stat(filename)
	if err != nil {
		if os.IsNotExist(err) {
			// 6 month ago
			return time.Now().AddDate(0, -6, 0), nil
		}
		return time.Time{}, err
	}

	// read the time from  the check file
	b, err := ioutil.ReadFile(filename)
	if err != nil {
		return time.Time{}, err
	}
	data := strings.TrimRight(string(b), "\n")

	// parse the string format to time.Time
	t, err := time.Parse(object.DateFormat, data)
	if err != nil {
		return time.Time{}, err
	}

	return t, nil
}

func writeCheckTime(filename string) error {
	// open the file with truncate flag
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()

	// write the checking time
	f.WriteString(start.Format(object.DateFormat))

	return nil
}

// prepare the result directory for storing the checking result every time
func prepareDir(dir string) error {
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return os.Mkdir(dir, os.ModePerm)
		}
		return err
	}
	return nil
}

func main() {
	// prepare the result directory for storing the checking result every time
	if err := prepareDir(resultDir); err != nil {
		panic(err)
	}

	// read the last checking time
	lastTime, err := readCheckTime(checkFileName)
	if err != nil {
		panic(err)
	}

	var conf Config
	// parse the config file
	if err := parseYamlFile(configFileName, &conf); err != nil {
		panic(err)
	}

	// retrieve all the repositories
	repositories, err := retrieveRepositories(&conf)
	if err != nil {
		panic(err)
	}

	filename := fmt.Sprintf("%s/%s.txt", resultDir, start.Format("2006-01-02"))
	// create a new result file every date with flag truncate | create | read-write
	fd, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		panic(err)
	}
	defer fd.Close()

	defer func() {
		// clean the local temporary repository
		for _, path := range cleanPaths {
			os.RemoveAll(path)
		}
	}()

	// loop the repositories and check the config files
	for repos, url := range repositories {
		fmt.Printf("local path: %v, url: %v\n", filepath.Join(conf.Clone, repos), url)

		// check if the config files in repositories is changed
		if err := checkRepository(&conf, repos, url, lastTime, fd); err != nil {
			panic(err)
		}

		fmt.Printf("\n")
	}

	// update the checking time for next checking
	if err := writeCheckTime(checkFileName); err != nil {
		panic(err)
	}
}
