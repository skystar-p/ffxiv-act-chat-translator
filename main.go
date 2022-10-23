package main

/*
example log line:
  00|2022-10-22T20:13:42.0000000+09:00|003D|ナブシェーラ|今でもときどき、あの化け物に襲われる夢を見てしまうの。|c83d39a60fdda387
*/

/*
some chat codes:
  SAY                   = "000A"
  SHOUT                 = "000B"
  PARTY                 = "000E"
  ALLIANCE              = "000F"
  LINKSHELL1            = "0010"
  LINKSHELL2            = "0011"
  LINKSHELL3            = "0012"
  LINKSHELL4            = "0013"
  LINKSHELL5            = "0014"
  LINKSHELL6            = "0015"
  LINKSHELL7            = "0016"
  LINKSHELL8            = "0017"
  FREE_COMPANY          = "0018"
  CUSTOM_EMOTE          = "001C"
  EMOTE                 = "001D"
  YELL                  = "001E"
  WHISPER               = "000C"
  CROSSWORLD_LINKSHELL1 = "0025"
  CROSSWORLD_LINKSHELL2 = "0065"
  CROSSWORLD_LINKSHELL3 = "0066"
  CROSSWORLD_LINKSHELL4 = "0067"
  CROSSWORLD_LINKSHELL5 = "0068"
*/

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/caarlos0/env/v6"
	"github.com/hpcloud/tail"
	"github.com/sirupsen/logrus"
)

type ConfigStruct struct {
	LogDirectory    string   `env:"LOG_DIRECTORY"`
	TargetChatCodes []string `env:"TARGET_CHATCODES"`
	SecretFileName  string   `env:"SECRET_FILE_NAME" envDefault:"secret.json"`
	SourceLanguage  string   `env:"SOURCE_LANGUAGE" envDefault:"ja"`
	TargetLanguage  string   `env:"TARGET_LANGUAGE" envDefault:"ko"`

	TranslateActorName bool `env:"TRANSLATE_ACTOR_NAME" envDefault:"false"`
}

type Secret struct {
	ClientId     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

var (
	Config ConfigStruct
)

func parseEnv() error {
	if err := env.Parse(&Config); err != nil {
		return err
	}
	return nil
}

var secretIdx int = 0

func main() {
	// parse env
	if err := parseEnv(); err != nil {
		logrus.WithError(err).Fatalf("failed to parse env")
	}

	logrus.Infof("using config: %+v", Config)

	var secret []Secret
	// read secret.json file
	b, err := os.ReadFile(Config.SecretFileName)
	if err != nil {
		logrus.WithError(err).Fatal(err)
	}
	if err := json.Unmarshal(b, &secret); err != nil {
		logrus.WithError(err).Fatal(err)
	}

	// find most recent log file
	logFiles, err := os.ReadDir(Config.LogDirectory)
	if err != nil {
		logrus.WithError(err).Fatal("failed to read log directory")
	}
	var (
		logFileName string
		logFileTime time.Time
	)
	for _, f := range logFiles {
		if f.IsDir() {
			continue
		}
		if !strings.HasSuffix(f.Name(), ".log") {
			continue
		}
		info, err := f.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(logFileTime) {
			logFileName = f.Name()
			logFileTime = info.ModTime()
		}
	}
	logrus.Infof("using most recent log file: %s", logFileName)

	now := time.Now()

	t, err := tail.TailFile(path.Join(Config.LogDirectory, logFileName), tail.Config{Follow: true})
	if err != nil {
		logrus.WithError(err).Fatal("tail file error")
	}

	for line := range t.Lines {
		l, err := parseLine(line.Text)
		if err != nil {
			continue
		}

		if l.Timestamp.Before(now) {
			continue
		}

		// check if log code is in target chat codes
		var foundChatCode bool
		for _, targetChatCode := range Config.TargetChatCodes {
			if l.LogCode == targetChatCode {
				foundChatCode = true
				break
			}
		}
		if !foundChatCode {
			continue
		}

		// translate
		var (
			actor string
		)
		if Config.TranslateActorName {
			translated, err := translate(secret, &secretIdx, l.ActorName)
			if err != nil {
				logrus.WithError(err).Error("translate error when translating actor name")
				actor = l.ActorName
			} else {
				actor = translated
			}
		} else {
			actor = l.ActorName
		}
		translated, err := translate(secret, &secretIdx, l.Content)
		if err != nil {
			logrus.WithError(err).Error("translate error")
		}
		fmt.Printf("%s: %s\n\n", actor, translated)
	}
}

type Log struct {
	Timestamp time.Time
	LogCode   string
	ActorName string
	Content   string
}

func parseLine(line string) (Log, error) {
	s := strings.Split(line, "|")
	if len(s) < 5 {
		return Log{}, fmt.Errorf("invalid line: %s", line)
	}

	t, err := time.Parse("2006-01-02T15:04:05.0000000-07:00", s[1])
	if err != nil {
		return Log{}, err
	}
	l := Log{
		Timestamp: t,
		LogCode:   s[2],
		ActorName: s[3],
		Content:   s[4],
	}

	return l, nil
}

const (
	papagoAPIEndpoint = "https://openapi.naver.com/v1/papago/n2mt"
)

var (
	httpClient = &http.Client{
		Timeout: 10 * time.Second,
	}
)

type PapagoResponse struct {
	Message struct {
		Result struct {
			TranslatedText string `json:"translatedText"`
		} `json:"result,omitempty"`
	} `json:"message"`
	ErrorMsg  string `json:"errorMessage,omitempty"`
	ErrorCode string `json:"errorCode,omitempty"`
}

var (
	translateCache = make(map[string]string)
)

func translate(secrets []Secret, secretIdx *int, content string) (string, error) {
	if content == "" {
		return "", nil
	}
	// check translation cache
	if translated, ok := translateCache[content]; ok {
		logrus.Debugf("using translation cache: %s", content)
		return translated, nil
	}

	data := strings.NewReader(fmt.Sprintf(`source=%s&target=%s&text=%s`, Config.SourceLanguage, Config.TargetLanguage, content))
	req, err := http.NewRequest("POST", papagoAPIEndpoint, data)
	if err != nil {
		return "", err
	}
	secret := secrets[*secretIdx]
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-Naver-Client-Id", secret.ClientId)
	req.Header.Set("X-Naver-Client-Secret", secret.ClientSecret)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var papagoResp PapagoResponse
	if err := json.Unmarshal(b, &papagoResp); err != nil {
		return "", err
	}
	if papagoResp.ErrorMsg != "" {
		if strings.Contains(papagoResp.ErrorMsg, "exceeded") {
			*secretIdx++
			logrus.Infof("query limit exceeded. rotating secret... (idx %d)", *secretIdx)
			return translate(secrets, secretIdx, content)
		}
		return "", fmt.Errorf("papago error: %s", papagoResp.ErrorMsg)
	}

	result := papagoResp.Message.Result.TranslatedText

	translateCache[content] = result

	return result, nil
}
