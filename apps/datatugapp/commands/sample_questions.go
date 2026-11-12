package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/datatug/datatug-cli/pkg/chat"
	"github.com/datatug/datatug-cli/pkg/secureread"
)

// A first-time user of the sample project gets an answer to a question in
// plain words before any AI is set up: four ready-made questions, each a
// query written here that runs through the same policy-bound executor as any
// other read of the chat. No model is asked and nothing leaves the machine.
type sampleQuestion struct {
	ask   string // the question as it is offered and matched
	title string // the title of its result
	tugql string
}

var sampleQuestions = []sampleQuestion{
	{
		ask: "Which countries bring in the most revenue?", title: "Revenue by country",
		tugql: `from:
  name: Invoice
groupBy:
  - field: BillingCountry
columns:
  - field: BillingCountry
    as: Country
  - aggregate:
      function: sum
      args:
        - field: Total
    as: Revenue
  - aggregate:
      function: count
      args:
        - star: true
    as: Invoices
orderBy:
  - field: Revenue
    desc: true
limit: 10
`,
	},
	{
		ask: "Which genres have the most tracks?", title: "Tracks by genre",
		tugql: `from:
  name: Track
  alias: t
  joins:
    - from:
        name: Genre
        alias: g
      on:
        - left:
            field: GenreId
            source: t
          op: '=='
          right:
            field: GenreId
            source: g
groupBy:
  - field: Name
    source: g
columns:
  - field: Name
    source: g
    as: Genre
  - aggregate:
      function: count
      args:
        - star: true
    as: Tracks
orderBy:
  - aggregate:
      function: count
      args:
        - star: true
    desc: true
limit: 10
`,
	},
	{
		ask: "Which artists have the most albums?", title: "Albums by artist",
		tugql: `from:
  name: Album
  alias: al
  joins:
    - from:
        name: Artist
        alias: ar
      on:
        - left:
            field: ArtistId
            source: al
          op: '=='
          right:
            field: ArtistId
            source: ar
groupBy:
  - field: Name
    source: ar
columns:
  - field: Name
    source: ar
    as: Artist
  - aggregate:
      function: count
      args:
        - star: true
    as: Albums
orderBy:
  - aggregate:
      function: count
      args:
        - star: true
    desc: true
limit: 10
`,
	},
	{
		ask: "What are the ten longest tracks?", title: "Longest tracks",
		tugql: `from:
  name: Track
columns:
  - field: Name
    as: Track
  - field: Milliseconds
orderBy:
  - field: Milliseconds
    desc: true
limit: 10
`,
	},
}

// sampleSignInAdvice is how the sample gets real AI. Hosted AI is the person's
// own choice, made by naming it: nothing switches a project to it by itself.
//
// The command names the sample, and the text says that the choice is
// remembered: --model is saved for the next chats, whatever project they open,
// so a person must know how to go back before they open their own data.
const sampleSignInAdvice = "To ask your own questions with DataTug's hosted AI, quit and run: datatug auth login, then: datatug chat --project " + sampleProjectID + " --model cloud. A personal Free plan includes a monthly allowance of questions. DataTug remembers that model for your next chats, on any project; datatug chat --model none goes back to local."

// sampleQuestionsList is the numbered list of the ready-made questions.
func sampleQuestionsList() string {
	lines := make([]string, len(sampleQuestions))
	for i, q := range sampleQuestions {
		lines[i] = fmt.Sprintf("%d. %s", i+1, q.ask)
	}
	return strings.Join(lines, "\n")
}

// sampleQuestionsNotice is the line the sample opens with when no AI is set up.
func sampleQuestionsNotice() string {
	return "No AI is set up yet, but this sample answers four ready-made questions without it. Type a number and press Enter:\n" + sampleQuestionsList() + "\n" + sampleSignInAdvice
}

// sampleConversation answers the ready-made questions of the sample project.
type sampleConversation struct {
	// run executes one TugQL document against the sample database.
	run      func(ctx context.Context, tugql []byte) (secureread.Result, error)
	source   string // the source URL of the sample database
	sourceID string // its catalog ID
}

// matchSampleQuestion finds the ready-made question a message asks for: its
// number, or its text whatever the case and the closing punctuation.
func matchSampleQuestion(message string) (sampleQuestion, bool) {
	normal := func(s string) string {
		return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "?.! "))
	}
	asked := normal(message)
	for i, q := range sampleQuestions {
		if asked == fmt.Sprint(i+1) || asked == normal(q.ask) {
			return q, true
		}
	}
	return sampleQuestion{}, false
}

func (c sampleConversation) AskWithContext(ctx context.Context, message, _ string) (chat.Turn, error) {
	question, ok := matchSampleQuestion(message)
	if !ok {
		return chat.Turn{Text: "I can't answer that yet: no AI is set up. Without AI this sample answers these — type a number:\n" + sampleQuestionsList() + "\n" + sampleSignInAdvice + " No query was executed."}, nil
	}
	result, err := c.run(ctx, []byte(question.tugql))
	if err != nil {
		return chat.Turn{Text: fmt.Sprintf("The ready-made answer to %q could not be read: %s. These questions expect the sample database as it was installed.", question.ask, publicSampleError(err))}, nil
	}
	return chat.Turn{
		Text:    question.ask + "\nA ready-made answer: the query below was written in advance and no AI was used.",
		Queries: []chat.QueryResult{{Title: question.title, TugQL: question.tugql, Result: result, Source: c.source, SourceID: c.sourceID}},
	}, nil
}

// publicSampleError is an error of a sample read on one line.
func publicSampleError(err error) string {
	return strings.TrimRight(strings.Join(strings.Fields(err.Error()), " "), ".")
}
