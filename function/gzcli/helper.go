package gzcli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dimasma0305/ctfify/function/gzcli/gzapi"
	"github.com/dimasma0305/ctfify/function/log"
)

func findCurrentGame(games []gzapi.Game, title string) *gzapi.Game {
	for _, game := range games {
		if game.Title == title {
			return &game
		}
	}
	return nil
}

func createNewGame(config *Config) (*gzapi.Game, error) {
	log.Info("Create new game")
	event := gzapi.CreateGameForm{
		Title: config.Event.Title,
		Start: config.Event.Start,
		End:   config.Event.End,
	}
	game, err := api.CreateGame(event)
	if err != nil {
		return nil, err
	}
	if config.Event.Poster == "" {
		return nil, fmt.Errorf("poster is required")
	}

	poster, err := createPosterIfNotExistOrDifferent(config.Event.Poster, game, api)
	if err != nil {
		return nil, err
	}

	config.Event.Id = game.Id
	config.Event.PublicKey = game.PublicKey
	config.Event.Poster = poster
	if err := game.Update(&config.Event); err != nil {
		return nil, err
	}
	if err := setCache("config", config); err != nil {
		return nil, err
	}
	return game, nil
}

func updateGameIfNeeded(config *Config, currentGame *gzapi.Game) error {
	poster, err := createPosterIfNotExistOrDifferent(config.Event.Poster, currentGame, api)
	if err != nil {
		return err
	}
	config.Event.Poster = poster
	if fmt.Sprintf("%v", config.Event) != fmt.Sprintf("%v", *currentGame) {
		log.Info("Updated %s game", config.Event.Title)

		config.Event.Id = currentGame.Id
		config.Event.PublicKey = currentGame.PublicKey

		if err := currentGame.Update(&config.Event); err != nil {
			return err
		}
		if err := setCache("config", config); err != nil {
			return err
		}
	}
	return nil
}

func validateChallenges(challengesConf []ChallengeYaml) error {
	for _, challengeConf := range challengesConf {
		if challengeConf.Type == "" {
			challengeConf.Type = "StaticAttachments"
		}

		if err := isGoodChallenge(challengeConf); err != nil {
			return err
		}
	}
	return nil
}

func syncChallenge(config *Config, challengeConf ChallengeYaml, challenges []gzapi.Challenge) error {
	var challengeData *gzapi.Challenge
	var err error
	if !isChallengeExist(challengeConf.Name, challenges) {
		log.Info("Create challenge %s", challengeConf.Name)
		challengeData, err = config.Event.CreateChallenge(gzapi.CreateChallengeForm{
			Title: challengeConf.Name,
			Tag:   challengeConf.Tag,
			Type:  challengeConf.Type,
		})
		if err != nil {
			return fmt.Errorf("create challenge %s: %v", challengeConf.Name, err)
		}
	} else {
		log.Info("Update challenge %s", challengeConf.Name)
		if err := GetCache(challengeConf.Tag+"/"+challengeConf.Name+"/challenge", &challengeData); err != nil {
			challengeData, err = config.Event.GetChallenge(challengeConf.Name)
			if err != nil {
				return fmt.Errorf("get challenge %s: %v", challengeConf.Name, err)
			}
		}
	}

	err = handleChallengeAttachments(challengeConf, challengeData)
	if err != nil {
		return err
	}

	err = updateChallengeFlags(config, challengeConf, challengeData)
	if err != nil {
		return fmt.Errorf("update flags for %s: %v", challengeConf.Name, err)
	}

	challengeData = mergeChallengeData(&challengeConf, challengeData)
	if isConfigEdited(&challengeConf, challengeData) {
		if challengeData, err = challengeData.Update(*challengeData); err != nil {
			if strings.Contains(err.Error(), "404") {
				challengeData, err = config.Event.GetChallenge(challengeConf.Name)
				if err != nil {
					return fmt.Errorf("get challenge %s: %v", challengeConf.Name, err)
				}
				challengeData, err = challengeData.Update(*challengeData)
				if err != nil {
					return fmt.Errorf("update challenge %s: %v", challengeConf.Name, err)
				}
			}
		}
		fmt.Println(challengeData.Flags)

		if err := setCache(challengeData.Tag+"/"+challengeConf.Name+"/challenge", challengeData); err != nil {
			return err
		}
	} else {
		log.Info("Challenge %s is the same...", challengeConf.Name)
	}
	return nil
}

func handleChallengeAttachments(challengeConf ChallengeYaml, challengeData *gzapi.Challenge) error {
	if challengeConf.Provide != nil {
		if strings.HasPrefix(*challengeConf.Provide, "http") {
			log.Info("Create remote attachment for %s", challengeConf.Name)
			if err := challengeData.CreateAttachment(gzapi.CreateAttachmentForm{
				AttachmentType: "Remote",
				RemoteUrl:      *challengeConf.Provide,
			}); err != nil {
				return err
			}
		} else {
			return handleLocalAttachment(challengeConf, challengeData)
		}
	} else if challengeData.Attachment != nil {
		log.Info("Delete attachment for %s", challengeConf.Name)
		if err := challengeData.CreateAttachment(gzapi.CreateAttachmentForm{
			AttachmentType: "None",
		}); err != nil {
			return err
		}
	}
	return nil
}

func handleLocalAttachment(challengeConf ChallengeYaml, challengeData *gzapi.Challenge) error {
	log.Info("Create local attachment for %s", challengeConf.Name)
	zipFilename := hashString(*challengeConf.Provide) + ".zip"
	zipOutput := filepath.Join(challengeConf.Cwd, zipFilename)
	if info, err := os.Stat(filepath.Join(challengeConf.Cwd, *challengeConf.Provide)); err != nil || info.IsDir() {
		log.Info("Zip attachment for %s", challengeConf.Name)
		zipInput := filepath.Join(challengeConf.Cwd, *challengeConf.Provide)
		if err := zipSource(zipInput, zipOutput); err != nil {
			return err
		}
		challengeConf.Provide = &zipFilename
	}
	fileinfo, err := createAssetsIfNotExistOrDifferent(filepath.Join(challengeConf.Cwd, *challengeConf.Provide), api)
	if err != nil {
		return err
	}

	if challengeData.Attachment != nil && strings.Contains(challengeData.Attachment.Url, fileinfo.Hash) {
		log.Info("Attachment for %s is the same...", challengeConf.Name)
	} else {
		log.Info("Update attachment for %s", challengeConf.Name)
		if err := challengeData.CreateAttachment(gzapi.CreateAttachmentForm{
			AttachmentType: "Local",
			FileHash:       fileinfo.Hash,
		}); err != nil {
			return err
		}
	}
	os.Remove(zipOutput)
	return nil
}

func updateChallengeFlags(config *Config, challengeConf ChallengeYaml, challengeData *gzapi.Challenge) error {
	for _, flag := range challengeData.Flags {
		if !isExistInArray(flag.Flag, challengeConf.Flags) {
			flag.GameId = config.Event.Id
			flag.ChallengeId = challengeData.Id
			if err := flag.Delete(); err != nil {
				return err
			}
		}
	}

	isCreatingNewFlag := false

	for _, flag := range challengeConf.Flags {
		if !isFlagExist(flag, challengeData.Flags) {
			if err := challengeData.CreateFlag(gzapi.CreateFlagForm{
				Flag: flag,
			}); err != nil {
				return err
			}
			isCreatingNewFlag = true
		}
	}

	if isCreatingNewFlag {
		newChallData, err := challengeData.Refresh()
		if err != nil {
			return err
		}
		challengeData.Flags = newChallData.Flags
	}

	return nil
}

var shell = os.Getenv("SHELL")

func runScript(challengeConf ChallengeYaml, script string) error {
	if challengeConf.Scripts[script] == "" {
		return nil
	}
	cmd := exec.Command(shell, "-c", challengeConf.Scripts[script])
	cmd.Dir = challengeConf.Cwd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
