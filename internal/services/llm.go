package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/MaksimZadar/temail/internal/config"
	"github.com/MaksimZadar/temail/internal/llm"
	"github.com/MaksimZadar/temail/internal/secrets"
)

// LLMKeyStore holds provider API keys. secrets.Store satisfies it. Keys are
// read here and passed to the llm client. They are never returned to the
// frontend.
type LLMKeyStore interface {
	GetLLMKey(provider string) (string, error)
	SetLLMKey(provider, key string) error
	DeleteLLMKey(provider string) error
}

// Limits and timeouts for the LLM bound methods.
const (
	maxProviderName  = 64
	maxProviderModel = 200
	maxAPIKeyLen     = 4096
	llmCallTimeout   = 30 * time.Second
)

// providerNameRE limits names to what a keyring user key and an environment
// variable name can hold without surprises.
var providerNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Errors the LLM methods return. Messages are safe to show.
var (
	ErrProviderNotFound = errors.New("no LLM provider with that name")
	// ErrKeyNeedsReentry means the base URL changed while a stored key exists.
	// The key is not sent to a new host, so the user must enter it again.
	ErrKeyNeedsReentry = errors.New("the base URL changed; enter the API key again so it is not sent to the new address")
)

// LLMService manages LLM providers for the UI. It stores settings in
// config.json and keys in the keyring, and it calls a provider only for the
// explicit bound methods. No mail content flows through it.
type LLMService struct {
	cfg   ConfigStore
	keys  LLMKeyStore
	cfgMu sync.Mutex
}

// NewLLMService returns an LLMService.
func NewLLMService(cfg ConfigStore, keys LLMKeyStore) *LLMService {
	return &LLMService{cfg: cfg, keys: keys}
}

// LLMCapabilitiesDTO is the capability set of one provider.
type LLMCapabilitiesDTO struct {
	StructuredOutput bool `json:"structuredOutput"`
	Tools            bool `json:"tools"`
	MaxContext       int  `json:"maxContext"`
}

// LLMProviderDTO is one provider as the UI shows it. It never includes the key:
// HasKey only says whether one is available.
type LLMProviderDTO struct {
	Name         string             `json:"name"`
	BaseURL      string             `json:"baseURL"`
	Model        string             `json:"model"`
	Kind         string             `json:"kind"`
	Enabled      bool               `json:"enabled"`
	Capabilities LLMCapabilitiesDTO `json:"capabilities"`
	IsDefault    bool               `json:"isDefault"`
	HasKey       bool               `json:"hasKey"`
}

// LLMProviderInput is what SaveProvider accepts. APIKey is write-only: an empty
// APIKey keeps the stored key. The name identifies the provider and cannot be
// changed by an update.
type LLMProviderInput struct {
	Name         string             `json:"name"`
	BaseURL      string             `json:"baseURL"`
	Model        string             `json:"model"`
	Kind         string             `json:"kind"`
	Enabled      bool               `json:"enabled"`
	Capabilities LLMCapabilitiesDTO `json:"capabilities"`
	APIKey       string             `json:"apiKey"`
}

// ProviderTest is the outcome of TestProvider. Message never contains a key or
// any request content.
type ProviderTest struct {
	OK       bool   `json:"ok"`
	Category string `json:"category"`
	Message  string `json:"message"`
}

// Extra categories beyond the llm package's, for the Go-side checks. The
// keyring category is CategoryKeyringDown in services.go.
const (
	CategoryCloudDisabled = "cloud_disabled"
	CategoryNoModel       = "no_model"
)

// ListProviders returns every configured provider, the default one marked. It
// reads the keyring only to see whether a key exists.
func (s *LLMService) ListProviders() ([]LLMProviderDTO, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	out := make([]LLMProviderDTO, 0, len(cfg.LLM.Providers))
	for _, p := range cfg.LLM.Providers {
		_, kerr := s.keys.GetLLMKey(p.Name)
		out = append(out, LLMProviderDTO{
			Name:         p.Name,
			BaseURL:      p.BaseURL,
			Model:        p.Model,
			Kind:         p.EffectiveKind(),
			Enabled:      p.IsEnabled(),
			Capabilities: capsDTO(p.Capabilities),
			IsDefault:    p.Name == cfg.LLM.DefaultProvider,
			HasKey:       kerr == nil,
		})
	}
	return out, nil
}

// SaveProvider creates or updates a provider. The config is written first, then
// the key (if one was given). If the key write fails, the previous config is
// restored. A changed base URL needs a new key when one is stored: the old key
// is not sent to the new address.
func (s *LLMService) SaveProvider(in LLMProviderInput) error {
	name := strings.TrimSpace(in.Name)
	switch {
	case !providerNameRE.MatchString(name):
		return fmt.Errorf("%w: name must be 1 to %d letters, digits, '_' or '-', starting with a letter or digit", ErrInvalidInput, maxProviderName)
	case len(in.Model) > maxProviderModel:
		return fmt.Errorf("%w: model", ErrInvalidInput)
	case len(in.APIKey) > maxAPIKeyLen:
		return fmt.Errorf("%w: apiKey is too long", ErrInvalidInput)
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	old, err := s.loadConfig()
	if err != nil {
		return err
	}
	cfg := copyConfig(old)
	idx := findProvider(cfg, name)
	existed := idx >= 0
	enabled := in.Enabled
	p := config.LLMProvider{Name: name}
	if existed {
		p = cfg.LLM.Providers[idx]
	}
	if existed && p.BaseURL != strings.TrimSpace(in.BaseURL) && in.APIKey == "" {
		if _, kerr := s.keys.GetLLMKey(name); kerr == nil {
			return ErrKeyNeedsReentry
		}
	}
	p.BaseURL = strings.TrimSpace(in.BaseURL)
	p.Model = strings.TrimSpace(in.Model)
	p.Kind = in.Kind
	p.Enabled = &enabled
	p.Capabilities = capsFromDTO(in.Capabilities)
	if existed {
		cfg.LLM.Providers[idx] = p
	} else {
		cfg.LLM.Providers = append(cfg.LLM.Providers, p)
	}
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	if err := s.cfg.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if in.APIKey == "" {
		return nil
	}
	if err := s.keys.SetLLMKey(name, in.APIKey); err != nil {
		if rerr := s.cfg.Save(old); rerr != nil {
			return fmt.Errorf("store API key: %w (restoring the previous config also failed: %v)", err, rerr)
		}
		return fmt.Errorf("store API key: %w", err)
	}
	return nil
}

// DeleteProvider removes a provider and its keyring key. The config is written
// first, and restored if the keyring delete fails. Clearing the default is part
// of the same change.
func (s *LLMService) DeleteProvider(name string) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	old, err := s.loadConfig()
	if err != nil {
		return err
	}
	idx := findProvider(old, name)
	if idx < 0 {
		return ErrProviderNotFound
	}
	cfg := copyConfig(old)
	cfg.LLM.Providers = append(cfg.LLM.Providers[:idx], cfg.LLM.Providers[idx+1:]...)
	if cfg.LLM.DefaultProvider == name {
		cfg.LLM.DefaultProvider = ""
	}
	if err := s.cfg.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	if err := s.keys.DeleteLLMKey(name); err != nil {
		if rerr := s.cfg.Save(old); rerr != nil {
			return fmt.Errorf("delete API key: %w (restoring the previous config also failed: %v)", err, rerr)
		}
		return fmt.Errorf("delete API key: %w", err)
	}
	return nil
}

// SetDefaultProvider makes name the default provider. An empty name clears the
// default.
func (s *LLMService) SetDefaultProvider(name string) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	cfg, err := s.loadConfig()
	if err != nil {
		return err
	}
	if name != "" && findProvider(cfg, name) < 0 {
		return ErrProviderNotFound
	}
	cfg.LLM.DefaultProvider = name
	if err := s.cfg.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

// TestProvider checks the provider with one tiny structured call. It returns a
// category and a message. It returns an error only if the settings cannot be
// read.
func (s *LLMService) TestProvider(ctx context.Context, name string) (ProviderTest, error) {
	client, err := s.clientFor(name)
	if err != nil {
		if errors.Is(err, ErrProviderNotFound) {
			return ProviderTest{}, err
		}
		return categorizeLLM(err), nil
	}
	ctx, cancel := context.WithTimeout(ctx, llmCallTimeout)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		return categorizeLLM(err), nil
	}
	return ProviderTest{OK: true, Category: string(llm.CategoryOK), Message: "The provider answered in the requested format."}, nil
}

// ListModels returns the model IDs the provider lists, sorted. An endpoint that
// does not list models returns an empty list and an error whose message says
// so. The UI then asks for the model name.
func (s *LLMService) ListModels(ctx context.Context, name string) ([]string, error) {
	client, err := s.clientFor(name)
	if err != nil {
		return []string{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, llmCallTimeout)
	defer cancel()
	ids, err := client.ListModels(ctx)
	if err != nil {
		return []string{}, err
	}
	return ids, nil
}

// clientFor builds an llm client for a configured provider, with its key from
// the keyring (or the environment). The key stays inside the client.
func (s *LLMService) clientFor(name string) (*llm.Client, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	idx := findProvider(cfg, name)
	if idx < 0 {
		return nil, ErrProviderNotFound
	}
	key, kerr := s.keys.GetLLMKey(name)
	if kerr != nil && !errors.Is(kerr, secrets.ErrNotFound) {
		return nil, fmt.Errorf("%w: %v", secrets.ErrKeyringUnavailable, kerr)
	}
	client, err := llm.NewClient(llm.ProviderFrom(cfg.LLM.Providers[idx]), key, llm.Options{Timeout: llmCallTimeout})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	return client, nil
}

func (s *LLMService) loadConfig() (config.Config, error) {
	cfg, err := s.cfg.Load()
	if err != nil {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func findProvider(cfg config.Config, name string) int {
	for i, p := range cfg.LLM.Providers {
		if p.Name == name {
			return i
		}
	}
	return -1
}

func capsDTO(c config.LLMCapabilities) LLMCapabilitiesDTO {
	return LLMCapabilitiesDTO{StructuredOutput: c.StructuredOutput, Tools: c.Tools, MaxContext: c.MaxContext}
}

func capsFromDTO(c LLMCapabilitiesDTO) config.LLMCapabilities {
	return config.LLMCapabilities{StructuredOutput: c.StructuredOutput, Tools: c.Tools, MaxContext: c.MaxContext}
}

// categorizeLLM maps an error from the llm package to a ProviderTest.
func categorizeLLM(err error) ProviderTest {
	if err == nil {
		return ProviderTest{OK: true, Category: string(llm.CategoryOK), Message: "The provider answered in the requested format."}
	}
	cat, msg := string(llm.CategoryOf(err)), ""
	switch {
	case errors.Is(err, llm.ErrCloudDisabled):
		cat, msg = CategoryCloudDisabled, "This cloud provider is turned off. Enable it before testing or sending content."
	case errors.Is(err, llm.ErrNoModel):
		cat, msg = CategoryNoModel, "Set a model name for this provider first."
	case errors.Is(err, secrets.ErrKeyringUnavailable):
		cat, msg = CategoryKeyringDown, "The OS keychain is not available, so the API key cannot be read."
	case errors.Is(err, llm.ErrInvalidOutput):
		cat, msg = string(llm.CategoryStructuredUnsupported), "The provider did not return the structured format. Try turning off structuredOutput for it."
	default:
		msg = llmMessage(llm.CategoryOf(err))
	}
	return ProviderTest{OK: false, Category: cat, Message: msg}
}

func llmMessage(c llm.Category) string {
	switch c {
	case llm.CategoryUnreachable:
		return "The provider could not be reached. Check the base URL and that the service is running."
	case llm.CategoryUnauthorized:
		return "The provider rejected the API key."
	case llm.CategoryModelNotFound:
		return "The model or the endpoint path was not found."
	case llm.CategoryStructuredUnsupported:
		return "The provider does not return the structured format. Try turning off structuredOutput for it."
	case llm.CategoryTimeout:
		return "The provider did not answer in time."
	default:
		return "The provider request failed."
	}
}
