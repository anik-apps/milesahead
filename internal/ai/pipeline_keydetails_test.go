package ai

import (
	"reflect"
	"strings"
	"testing"
)

// generatorReply is a reply in the htmlFormat layout the generator prompts ask
// for, including the Markdown slips models still make in the body (bold, a "- "
// bullet list) that cleanMarkdown exists to repair.
const generatorReply = `Capital One 20% Transfer Bonus to British Airways Through October 31

TLDR: Capital One is offering a 20% bonus on transfers to British Airways Avios through October 31 - worth it if you have a BA redemption in mind.

KEY_DETAILS_START
- Bonus: 20% extra Avios on transfers
- Deadline: October 31, 2026
- Transfer ratio: 1,000 miles to 1,200 Avios
- Eligible cards: **Venture X** and other Capital One miles cards
KEY_DETAILS_END

LINKS_START
- Capital One Travel Partners | https://www.capitalone.com/learn-grow/money-management/venture-miles-transfer-partnerships/
- British Airways Executive Club | https://www.britishairways.com/executive-club
LINKS_END

<p>Capital One is running a <strong>20% transfer bonus</strong> to British Airways through October 31.</p>

<h3>Who should transfer</h3>

**Only transfer for a specific booking.** Good uses:
- Short-haul partner awards
- Off-peak Club World

<p>Transfers are usually instant.</p>`

// wantGeneratorReplyBody is the body HTML the pipeline produced for
// generatorReply before key details were parsed from the raw reply. Parsing
// them earlier must not change it.
const wantGeneratorReplyBody = "<p>Capital One is running a <strong>20% transfer bonus</strong> to British Airways through October 31.</p>\n\n" +
	"<h3>Who should transfer</h3>\n\n" +
	"<strong>Only transfer for a specific booking.</strong> Good uses:\n<li>Short-haul partner awards</li>\n<li>Off-peak Club World</li>\n\n" +
	"<p>Transfers are usually instant.</p>"

func keyDetailsPipeline(reply string, client *perModelMockHFClient) *Pipeline {
	if client.responses == nil {
		client.responses = map[string]string{}
	}
	client.responses["gen"] = reply
	client.responses["critic"] = cleanVerdict
	p := NewPipeline(NewGenerator(client, "gen", "", nil), NewCritic(client, "critic"), 80)
	p.SetDetailModels([]string{"details"})
	return p
}

func calledModel(c *perModelMockHFClient, model string) bool {
	for _, m := range c.calls() {
		if m == model {
			return true
		}
	}
	return false
}

// TestPipeline_Process_UsesGeneratorKeyDetails: the key details and links the
// generator wrote are parsed from its reply, so no extraction call is made.
func TestPipeline_Process_UsesGeneratorKeyDetails(t *testing.T) {
	client := &perModelMockHFClient{}
	res, err := keyDetailsPipeline(generatorReply, client).Process("deal", numberedRefSources())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if calledModel(client, "details") {
		t.Errorf("extraction call made although the reply carried key details; calls = %v", client.calls())
	}
	wantDetails := []KeyDetail{
		{Label: "Bonus", Value: "20% extra Avios on transfers"},
		{Label: "Deadline", Value: "October 31, 2026"},
		{Label: "Transfer ratio", Value: "1,000 miles to 1,200 Avios"},
		{Label: "Eligible cards", Value: "Venture X and other Capital One miles cards"},
	}
	if !reflect.DeepEqual(res.KeyDetails, wantDetails) {
		t.Errorf("key details = %+v\nwant %+v", res.KeyDetails, wantDetails)
	}
	wantLinks := []ArticleLink{
		{Text: "Capital One Travel Partners", URL: "https://www.capitalone.com/learn-grow/money-management/venture-miles-transfer-partnerships/"},
		{Text: "British Airways Executive Club", URL: "https://www.britishairways.com/executive-club"},
	}
	if !reflect.DeepEqual(res.Links, wantLinks) {
		t.Errorf("links = %+v\nwant %+v", res.Links, wantLinks)
	}

	if res.Content != wantGeneratorReplyBody {
		t.Errorf("body changed:\n got %q\nwant %q", res.Content, wantGeneratorReplyBody)
	}
	if res.Title != "Capital One 20% Transfer Bonus to British Airways Through October 31" {
		t.Errorf("title = %q", res.Title)
	}
	if !strings.HasPrefix(res.TLDR, "Capital One is offering a 20% bonus") {
		t.Errorf("tldr = %q", res.TLDR)
	}
}

// TestPipeline_Process_FallsBackToExtraction: a reply with no key details
// still gets them from the separate extraction call.
func TestPipeline_Process_FallsBackToExtraction(t *testing.T) {
	reply := "Capital One BA Bonus\n\nTLDR: 20% bonus.\n\n<p>Capital One is running a 20% bonus to British Airways.</p>"
	client := &perModelMockHFClient{responses: map[string]string{
		"details": `{"key_details": [{"Label": "Bonus", "Value": "20%"}], "links": [{"Text": "Capital One", "URL": "https://www.capitalone.com"}]}`,
	}}
	res, err := keyDetailsPipeline(reply, client).Process("deal", numberedRefSources())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !calledModel(client, "details") {
		t.Fatalf("no extraction call for a reply without key details; calls = %v", client.calls())
	}
	if len(res.KeyDetails) != 1 || res.KeyDetails[0].Value != "20%" {
		t.Errorf("key details = %+v", res.KeyDetails)
	}
	if len(res.Links) != 1 || res.Links[0].URL != "https://www.capitalone.com" {
		t.Errorf("links = %+v", res.Links)
	}
}

// TestPipeline_Process_KeyDetailsFollowAcceptedRevision: when the
// auto-revision replaces the article, its key details replace the first
// draft's too.
func TestPipeline_Process_KeyDetailsFollowAcceptedRevision(t *testing.T) {
	first := "Capital One BA Bonus\n\nTLDR: 20% bonus.\n\nKEY_DETAILS_START\n- Bonus: 25%\nKEY_DETAILS_END\n\nLINKS_START\nLINKS_END\n\n<p>Source 9 says 25%.</p>"
	revised := "Capital One BA Bonus\n\nTLDR: 20% bonus.\n\nKEY_DETAILS_START\n- Bonus: 20%\nKEY_DETAILS_END\n\nLINKS_START\nLINKS_END\n\n<p>The bonus is 20%.</p>"
	gen := &sequenceClient{replies: []string{first, revised}}
	critic := &recordingClient{reply: cleanVerdict}
	p := NewPipeline(NewGenerator(gen, "gen", "", nil), NewCritic(critic, "critic"), 80)

	res, err := p.Process("deal", numberedRefSources())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if gen.calls != 2 {
		t.Fatalf("generator called %d times; want generation then auto-revision", gen.calls)
	}
	if res.Content != "<p>The bonus is 20%.</p>" {
		t.Fatalf("revision not accepted; content = %q", res.Content)
	}
	if len(res.KeyDetails) != 1 || res.KeyDetails[0].Value != "20%" {
		t.Errorf("key details = %+v; want the revision's", res.KeyDetails)
	}
}

// sequenceClient returns its replies in order, repeating the last one.
type sequenceClient struct {
	replies []string
	calls   int
}

func (c *sequenceClient) Generate(model, prompt string, maxTokens int) (string, error) {
	i := c.calls
	if i >= len(c.replies) {
		i = len(c.replies) - 1
	}
	c.calls++
	return c.replies[i], nil
}

// TestPipeline_Process_GeneratorDetailWithUnmappableRef: a generator key
// detail citing a source number that maps to nothing fails the attribution
// check like any other reference, and the bullet itself is dropped.
func TestPipeline_Process_GeneratorDetailWithUnmappableRef(t *testing.T) {
	reply := "Capital One BA Bonus\n\nTLDR: 20% bonus.\n\nKEY_DETAILS_START\n- Bonus: 20%\n- Note: rare, per Source 7\nKEY_DETAILS_END\n\nLINKS_START\nLINKS_END\n\n<p>The bonus is 20%.</p>"
	client := &perModelMockHFClient{}
	res, err := keyDetailsPipeline(reply, client).Process("deal", numberedRefSources())
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if res.Verification.AttributionCheck {
		t.Error("attribution check passed with Source 7 in a key detail")
	}
	if len(res.KeyDetails) != 1 || res.KeyDetails[0].Label != "Bonus" {
		t.Errorf("key details = %+v; want the Source 7 bullet dropped", res.KeyDetails)
	}
	if calledModel(client, "details") {
		t.Errorf("extraction call made although a key detail survived; calls = %v", client.calls())
	}
}
