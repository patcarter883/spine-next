package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PiAPIOpenAI is the models.json api Spine can call itself.
const PiAPIOpenAI = "openai-completions"

// PiProvider is a provider from Pi's models.json.
type PiProvider struct {
	Name    string
	BaseURL string `json:"baseUrl"`
	API     string `json:"api"`
	APIKey  string `json:"apiKey"`
}

// LoadPiProvider reads one provider that Spine can call from the agent dir's
// models.json.
func LoadPiProvider(dir, name string) (PiProvider, error) {
	path := filepath.Join(dir, "models.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return PiProvider{}, err
	}
	var doc struct {
		Providers map[string]PiProvider `json:"providers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return PiProvider{}, fmt.Errorf("%s: %w", path, err)
	}
	p, ok := doc.Providers[name]
	if !ok {
		return PiProvider{}, fmt.Errorf("provider %q is not in %s", name, path)
	}
	if p.API != PiAPIOpenAI {
		return PiProvider{}, fmt.Errorf("provider %q uses api %q; only %s is supported here", name, p.API, PiAPIOpenAI)
	}
	if p.BaseURL == "" {
		return PiProvider{}, fmt.Errorf("provider %q has no baseUrl", name)
	}
	p.Name = name
	return p, nil
}

var piEnvRef = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$`)

// ResolveKey applies models.json's key forms: $NAME or ${NAME} from the
// environment, $$ and $! for a literal $ and !, otherwise literal. A !command
// is refused: Pi runs it in a shell, and Spine doesn't.
func (p PiProvider) ResolveKey() (string, error) {
	k := p.APIKey
	if strings.HasPrefix(k, "!") {
		return "", fmt.Errorf("provider %q gets its key from a command, which Spine won't run: use $NAME", p.Name)
	}
	if m := piEnvRef.FindStringSubmatch(k); m != nil {
		v, ok := os.LookupEnv(m[1])
		if !ok {
			return "", fmt.Errorf("provider %q reads $%s, which is not set", p.Name, m[1])
		}
		return v, nil
	}
	if strings.HasPrefix(k, "$$") || strings.HasPrefix(k, "$!") {
		return k[1:], nil
	}
	return k, nil
}
