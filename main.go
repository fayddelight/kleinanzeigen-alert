package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/danielstefank/kleinanzeigen-alert/pkg/model"
	"github.com/danielstefank/kleinanzeigen-alert/pkg/storage"
	"github.com/danielstefank/kleinanzeigen-alert/pkg/telegram"
)

var token string

var f = fmt.Sprintf

const fetchDuration = time.Minute * 5
const querySpacing = time.Second * 3

func main() {
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	debug := flag.Bool("debug", false, "sets log level to debug")

	flag.Parse()

	// Default level for this example is info, unless debug flag is present
	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if *debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr}).With().Caller().Logger().With().Timestamp().Logger()
	} else {
		log.Logger = log.With().Caller().Logger().With().Timestamp().Logger()
	}

	token = os.Getenv("TELEGRAM_APITOKEN")

	if token == "" {
		log.Panic().Msg("could read API token")
		os.Exit(1)
	}

	s := storage.NewStorage()
	defer s.CloseDB()
	bot := telegram.CreateBot(token, s)
	bot.Init()
	go bot.Start()

	// Seen ads are kept forever on purpose: pruning them (it used to drop ads
	// older than 7 days) re-alerted every ad still on page 1 a week later,
	// including old ads the seller bumped back to the top.
	// ponytail: ads table grows by only genuinely new ads (~tens/day); add a
	// prune of ads unseen for months if it ever gets big.

	for {
		queries := s.GetQueries()

		log.Info().Int("number_of_queries", len(queries)).Msg("fetching ads")
		for _, query := range queries {
			new, err := s.GetLatest(query.ID)

			// Failing queries are kept and retried, never deleted: a temporary
			// 403 block once wiped every query in two poll cycles.
			if err != nil {
				if !query.FailedPreviously {
					bot.SendMsg(query.ChatID, f("Anzeigen konnten für %s (ID: %d) nicht geladen werden. Ich versuche es weiter und melde mich, sobald es wieder klappt.", queryName(query), query.ID))
					s.UpdateQuery(query.ID, true)
				}
				time.Sleep(querySpacing)
				continue
			}

			if query.FailedPreviously {
				bot.SendMsg(query.ChatID, f("Anzeigen für %s (ID: %d) werden wieder geladen.", queryName(query), query.ID))
				s.UpdateQuery(query.ID, false)
			}

			log.Debug().Int("number_of_new_ads", len(new)).Msg("new ads found")
			err = bot.SendAds(query.ChatID, new, query)
			if err != nil {
				affected, err := s.RemoveByChatID(query.ChatID)
				if err != nil {
					log.Error().Err(err).
						Msg("could not remove  queries for blocked/deactivated user")
				} else {
					log.Info().
						Int("number_of_removed_queries", affected).
						Msg("removed queries for blocked/deactivated user")
				}
			}

			time.Sleep(querySpacing)
		}

		time.Sleep(fetchDuration)
	}
}

func queryName(q model.Query) string {
	if q.CustomLink != nil {
		return *q.CustomLink
	}
	return q.Term
}
