package main

import (
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

// This bot's unique command prefix for message parsing
const CMD_PREFIX = "kq!"

// Discord Bot token
var Token string

// Ongoing keeps track of active quizzes and the channels they belong to
var Ongoing struct {
	sync.RWMutex
	ChannelID map[string]bool
}

// Bot owner account
var Owner *discordgo.User

// Bot startup time
var TimeStarted = time.Now()

func init() {

	flag.StringVar(&Token, "t", "", "Bot Token")
	flag.Parse()

	// New seed for random in order to shuffle properly
	rand.Seed(time.Now().UnixNano())
	Ongoing.ChannelID = make(map[string]bool)

}

func main() {

	// Make sure we start with a token supplied
	if len(Token) == 0 {
		flag.Usage()
		return
	}

	// Initiate a new session using Bot Token for authentication
	session, err := discordgo.New("Bot " + Token)

	if err != nil {
		log.Println("ERROR, Failed to create Discord session:", err)
		return
	}

	// Register the messageCreate func as a callback for MessageCreate events
	session.AddHandler(messageCreate)

	// Open a websocket connection to Discord and begin listening
	err = session.Open()
	if err != nil {
		log.Println("ERROR, Couldn't open websocket connection:", err)
		return
	}

	// Figure out the owner of the bot for admin commands
	app, err := session.Application("@me")
	if err != nil {
		log.Println("ERROR, Couldn't get app:", err)
		return
	}
	Owner = app.Owner

	// Wait here until CTRL-C or other term signal is received
	log.Println("NOTICE, Bot is now running. Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)
	<-sc

	// Cleanly close down the Discord session.
	session.Close()
}

// This function will be called (due to AddHandler above) every time a new
// message is created on any channel that the autenticated bot has access to
func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {

	// Handle bot's own ping-pong messages
	if m.Author.ID == s.State.User.ID && strings.HasPrefix(m.Content, "Latency:") {
		parts := strings.Fields(m.Content)
		if len(parts) == 2 {
			oldtime, err := strconv.Atoi(parts[1])
			if err != nil {
				log.Println("ERROR, With bot ping:", err)
			}

			t := time.Since(time.Unix(0, int64(oldtime)))
			t -= t % time.Millisecond
			msgEdit(s, m, fmt.Sprintf("Latency: **%s** ", t))
		}
	}

	// Ignore all messages created bots
	if m.Author.Bot {
		return
	}

	// Only react on #bot* channels
	if ch, err := s.Channel(m.ChannelID); err != nil || !strings.HasPrefix(ch.Name, "bot") {
		if err != nil {
			log.Println("ERROR, With bot channel stuff:", err)
		}
		return
	}

	// Split up the message to parse the input string
	input := strings.Fields(strings.ToLower(strings.TrimSpace(m.Content)))
	var command string
	if len(input) >= 1 {
		command = input[0]
	}

	switch command {
	case CMD_PREFIX + "help":
		showHelp(s, m)
	case CMD_PREFIX + "uptime":
		if m.Author.ID == Owner.ID {
			t := time.Since(TimeStarted)
			t -= t % time.Second
			msgSend(s, m, fmt.Sprintf("Uptime: **%s** ", t))
		} else {
			msgSend(s, m, "オーナーさんに　ちょうせん　なんて　10000こうねん　はやいんだよ！　"+m.Author.Mention())
		}
	case CMD_PREFIX + "ping":
		msgSend(s, m, fmt.Sprintf("Latency: %d", time.Now().UnixNano()))
	case CMD_PREFIX + "time":
		msgSend(s, m, fmt.Sprintf("Time is: **%s**", time.Now().In(time.UTC)))
	case CMD_PREFIX + "hello":
		imgSend(s, m, "Hello!")
	case CMD_PREFIX + "fast":
		if len(input) == 2 {
			go runQuiz(s, m, input[1], "", "0")
		} else if len(input) == 3 {
			go runQuiz(s, m, input[1], input[2], "0")
		} else if !hasQuiz(m) {
			// Show help unless already running, since that's handled elsewhere
			showHelp(s, m)
		}
	case CMD_PREFIX + "slow":
		if len(input) == 2 {
			go runQuiz(s, m, input[1], "", "5000")
		} else if len(input) == 3 {
			go runQuiz(s, m, input[1], input[2], "5000")
		} else if !hasQuiz(m) {
			// Show help unless already running, since that's handled elsewhere
			showHelp(s, m)
		}
	case CMD_PREFIX + "quiz":
		if len(input) == 2 {
			go runQuiz(s, m, input[1], "", "")
		} else if len(input) == 3 {
			go runQuiz(s, m, input[1], input[2], "")
		} else if !hasQuiz(m) {
			// Show help unless already running, since that's handled elsewhere
			showHelp(s, m)
		}
	}

	// Mostly a test to see if it reacts on mentions
	for _, u := range m.Mentions {
		if u.ID == s.State.User.ID {
			msgSend(s, m, "何故にボク、"+m.Author.Mention()+"？！")
		}
	}

}

// Show bot help message in channel
func showHelp(s *discordgo.Session, m *discordgo.MessageCreate) {
	quizlist := GetQuizlist()
	sort.Strings(quizlist)
	msgSend(s, m, fmt.Sprintf("Available quizzes: ```%s```\nUse `%squiz <name> [max score]` to start.", strings.Join(quizlist, ", "), CMD_PREFIX))
}

// Stop ongoing quiz in given channel
func stopQuiz(s *discordgo.Session, m *discordgo.MessageCreate) {
	count := 0

	Ongoing.Lock()
	delete(Ongoing.ChannelID, m.ChannelID)
	count = len(Ongoing.ChannelID)
	Ongoing.Unlock()

	// Update bot's user status to reflect running quizzes
	var status string
	if count == 1 {
		status = "1 quiz"
	} else if count >= 2 {
		status = fmt.Sprintf("%d quizzes", count)
	}

	err := s.UpdateStatus(0, status)
	if err != nil {
		log.Println("ERROR, Could not update status:", err)
	}
}

// Start ongoing quiz in given channel
func startQuiz(s *discordgo.Session, m *discordgo.MessageCreate) (err error) {
	count := 0

	Ongoing.Lock()
	_, exists := Ongoing.ChannelID[m.ChannelID]
	if !exists {
		Ongoing.ChannelID[m.ChannelID] = true
	} else {
		err = fmt.Errorf("Channel quiz already ongoing")
	}
	count = len(Ongoing.ChannelID)
	Ongoing.Unlock()

	// Update bot's user status to reflect running quizzes
	var status string
	if count == 1 {
		status = "1 quiz"
	} else if count >= 2 {
		status = fmt.Sprintf("%d quizzes", count)
	}

	err2 := s.UpdateStatus(0, status)
	if err2 != nil {
		log.Println("ERROR, Could not update status:", err2)
	}

	return
}

// Checks if given channel has ongoing quiz
func hasQuiz(m *discordgo.MessageCreate) bool {
	Ongoing.RLock()
	_, exists := Ongoing.ChannelID[m.ChannelID]
	Ongoing.RUnlock()

	return exists
}

// Run slow given quiz loop in given channel
func runQuiz(s *discordgo.Session, m *discordgo.MessageCreate, quizname string, winLimitGiven string, waitTimeGiven string) {

	// Mark the quiz as started
	if err := startQuiz(s, m); err != nil {
		// Quiz already running, nothing to do here
		return
	}

	quizChannel := m.ChannelID
	winLimit := 15                      // winner score
	timeout := 20                       // seconds to wait per round
	timeoutLimit := 5                   // count before aborting
	waitTime := 1250 * time.Millisecond // delay before closing round

	// Parse provided winLimit with sane defaults
	if i, err := strconv.Atoi(winLimitGiven); err == nil {
		if i <= 100 && i > 0 {
			winLimit = i
		}
	}

	// Parse provided waitTime with sane defaults
	if i, err := strconv.Atoi(waitTimeGiven); err == nil {
		if i <= 20000 && i >= 0 {
			waitTime = time.Duration(i) * time.Millisecond
		}
	}

	quiz := LoadQuiz(quizname)
	if len(quiz) == 0 {
		msgSend(s, m, "Failed to find quiz: "+quizname)
		stopQuiz(s, m)
		return
	}

	c := make(chan *discordgo.MessageCreate, 100)
	quitChan := make(chan struct{}, 100)

	killHandler := s.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		// Ignore all messages created by the bot itself
		// This isn't required in this specific example but it's a good practice.
		if m.Author.ID == s.State.User.ID || m.Author.Bot {
			return
		}

		// Only react on current quiz channel
		if m.ChannelID != quizChannel {
			return
		}

		// Handle quiz aborts
		if strings.ToLower(strings.TrimSpace(m.Content)) == CMD_PREFIX+"quiz" {
			quitChan <- struct{}{}
			return
		}

		// Relay the message to the quiz loop
		c <- m
	})

	msgSend(s, m, fmt.Sprintf("```Starting new kanji quiz (%d words) in 5 seconds;\ngive your answer in HIRAGANA! First to %d points wins.```", len(quiz), winLimit))

	var quizHistory string
	players := make(map[string]int)
	var timeoutCount int

	// Helper function to force katakana to hiragana conversion
	k2h := func(r rune) rune {
		switch {
		case r >= 'ァ' && r <= 'ヶ':
			return r - 0x60
		}
		return r
	}

outer:
	for len(quiz) > 0 {
		time.Sleep(5 * time.Second)

		// Grab new word from the quiz
		var current Question
		current, quiz = quiz[len(quiz)-1], quiz[:len(quiz)-1]

		// Replace reading with hiragana-only version
		current.Reading = strings.Map(k2h, current.Reading)

		// Add word to quiz history
		quizHistory += current.Word + "　" // Japanese space (wider)

		// Round's score keeper
		scoreKeeper := make(map[string]int)

		// Send out quiz question
		imgSend(s, m, current.Word)

		// Set timeout for no correct answers
		timeoutChan := time.NewTimer(time.Duration(timeout) * time.Second)

	inner:
		for {

			select {
			case <-quitChan:
				break outer
			case <-timeoutChan.C:
				if len(scoreKeeper) > 0 {
					break inner
				}

				msgSend(s, m, fmt.Sprintf(":no_entry: Timed out!\nCorrect answer: **%s** (%s)", current.Reading, current.Word))
				timeoutCount++
				if timeoutCount >= timeoutLimit {
					msgSend(s, m, "```Too many timeouts in a row reached, aborting quiz.```")
					break outer
				}
				break inner
			case msg := <-c:
				user := msg.Author
				if msg.Content == current.Reading {
					if len(scoreKeeper) == 0 {
						timeoutChan.Reset(waitTime)
					}

					// Make sure we don't add the same user again
					if _, exists := scoreKeeper[user.ID]; !exists {
						scoreKeeper[user.ID] = len(scoreKeeper) + 1
					}

					// Reset timeouts since we're active
					timeoutCount = 0
				}
			}
		}

		if len(scoreKeeper) > 0 {

			winnerExists := false
			var fastest string
			for player, position := range scoreKeeper {
				players[player]++
				if position == 1 {
					fastest = player
				}
				if players[player] >= winLimit {
					winnerExists = true
				}
			}

			var extras string
			if len(scoreKeeper) > 1 {
				extras = fmt.Sprintf(" (+%d)", len(scoreKeeper)-1)
			}

			msgSend(s, m, fmt.Sprintf(":white_check_mark: <@%s>%s got it right: **%s** (%s)", fastest, extras, current.Reading, current.Word))

			if winnerExists {
				break outer
			}
		}

	}

	// Clean up
	killHandler()

	// Produce scoreboard
	fields := make([]*discordgo.MessageEmbedField, 0, 2)
	var winners string
	var participants string

	for _, p := range ranking(players) {
		if p.Score >= winLimit {
			winners += fmt.Sprintf("<@%s>: %d points\n", p.Name, p.Score)
		} else {
			participants += fmt.Sprintf("<@%s>: %d point(s)\n", p.Name, p.Score)
		}
	}

	if len(winners) > 0 {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Winner",
			Value:  winners,
			Inline: false,
		})
	}

	if len(participants) > 0 {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   "Participants",
			Value:  participants,
			Inline: false,
		})
	}

	// Sleep for a little breathing room
	time.Sleep(1 * time.Second)

	embed := &discordgo.MessageEmbed{
		Type:        "rich",
		Title:       "Final Quiz Scoreboard: " + quizname,
		Description: "-------------------------------",
		Color:       0x33FF33,
		Fields:      fields,
		Footer:      &discordgo.MessageEmbedFooter{Text: quizHistory},
	}

	embedSend(s, m, embed)

	stopQuiz(s, m)
}

// Player type for ranking list
type Player struct {
	Name  string
	Score int
}

// Sort the player ranking list
func ranking(players map[string]int) (result []Player) {

	for k, v := range players {
		result = append(result, Player{k, v})
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Score > result[j].Score })

	return
}

// Send a given message to channel
func msgSend(s *discordgo.Session, m *discordgo.MessageCreate, msg string) {
	_, err := s.ChannelMessageSend(m.ChannelID, msg)
	if err != nil {
		log.Println("ERROR, Could not send message: ", err)
	}
}

// Send an image message to Discord
func imgSend(s *discordgo.Session, m *discordgo.MessageCreate, word string) {

	image := GenerateImage(word)

	_, err := s.ChannelFileSend(m.ChannelID, "word.png", image)
	if err != nil {
		log.Println("ERROR, Could not send image:", err)
		return
	}

}

// Send an embedded message type to Discord
func embedSend(s *discordgo.Session, m *discordgo.MessageCreate, embed *discordgo.MessageEmbed) {

	_, err := s.ChannelMessageSendEmbed(m.ChannelID, embed)
	if err != nil {
		log.Println("ERROR, Could not send embed:", err)
		return
	}

}

// Edit a given message on a channel
func msgEdit(s *discordgo.Session, m *discordgo.MessageCreate, msg string) {
	_, err := s.ChannelMessageEdit(m.ChannelID, m.ID, msg)
	if err != nil {
		log.Println("ERROR, Could not edit message: ", err)
	}
}
