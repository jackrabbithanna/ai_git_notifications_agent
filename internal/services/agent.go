package services

import (
	"context"
	"errors"

	"gitinbox/internal/agent"
	"gitinbox/internal/app"
	"gitinbox/internal/pipeline"
	"gitinbox/internal/store"
)

// AgentService drives the pi sidecar from the Agent view (M6).
type AgentService struct {
	App *app.App
}

// AgentStatus is the manager status plus the settings and how pi was located.
type AgentStatus struct {
	Settings pipeline.AgentSettings `json:"settings"`
	Status   agent.Status           `json:"status"`
	Disabled bool                   `json:"disabled"`
}

func (s *AgentService) Status() (AgentStatus, error) {
	ctx := context.Background()
	settings, err := s.App.Pipe.AgentSettings(ctx)
	if err != nil {
		return AgentStatus{}, err
	}
	out := AgentStatus{Settings: settings}
	m, err := s.App.AgentManager(ctx)
	if err != nil {
		out.Disabled = errors.Is(err, app.ErrAgentDisabled)
		out.Status = agent.Status{Error: err.Error()}
		return out, nil
	}
	out.Status = m.Status()
	return out, nil
}

// Locate reports which pi binary would be used and its version.
func (s *AgentService) Locate() (agent.Info, error) {
	settings, err := s.App.Pipe.AgentSettings(context.Background())
	if err != nil {
		return agent.Info{}, err
	}
	info, err := agent.Locate(settings.PiPath)
	if err != nil {
		return agent.Info{}, err
	}
	if v, err := agent.Version(context.Background(), info.Path); err == nil {
		info.Version = v
	}
	return info, nil
}

func (s *AgentService) Start() error { return s.App.StartAgent(context.Background()) }

func (s *AgentService) Stop() error {
	s.App.StopAgent()
	return nil
}

// Prompt sends a message (starting pi when needed); the reply streams as agent:event events.
func (s *AgentService) Prompt(text string) error {
	m, err := s.App.AgentManager(context.Background())
	if err != nil {
		return err
	}
	return m.Prompt(context.Background(), text)
}

func (s *AgentService) Abort() error {
	m, err := s.App.AgentManager(context.Background())
	if err != nil {
		return err
	}
	return m.Abort(context.Background())
}

func (s *AgentService) NewSession() error {
	m, err := s.App.AgentManager(context.Background())
	if err != nil {
		return err
	}
	return m.NewSession(context.Background())
}

func (s *AgentService) Transcript() ([]agent.Message, error) {
	m, err := s.App.AgentManager(context.Background())
	if err != nil {
		return nil, err
	}
	t := m.Transcript()
	if t == nil {
		t = []agent.Message{}
	}
	return t, nil
}

func (s *AgentService) Models() ([]agent.ModelInfo, error) {
	m, err := s.App.AgentManager(context.Background())
	if err != nil {
		return nil, err
	}
	return m.Models(context.Background())
}

// SetModel switches the running sidecar's model and remembers it in settings.
func (s *AgentService) SetModel(id string) error {
	ctx := context.Background()
	settings, err := s.App.Pipe.AgentSettings(ctx)
	if err != nil {
		return err
	}
	settings.Model = id
	if err := s.App.Pipe.SetAgentSettings(ctx, settings); err != nil {
		return err
	}
	m, err := s.App.AgentManager(ctx)
	if err != nil {
		return err
	}
	return m.SetModel(ctx, id)
}

func (s *AgentService) Settings() (pipeline.AgentSettings, error) {
	return s.App.Pipe.AgentSettings(context.Background())
}

func (s *AgentService) SaveSettings(st pipeline.AgentSettings) error {
	if err := s.App.Pipe.SetAgentSettings(context.Background(), st); err != nil {
		return err
	}
	if !st.Enabled {
		s.App.StopAgent()
	}
	return nil
}

// Draft returns the agent's draft reply for a thread, or nil.
func (s *AgentService) Draft(accountID int64, threadID string) (*store.Draft, error) {
	d, err := s.App.DB.GetDraft(context.Background(), accountID, threadID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *AgentService) DeleteDraft(accountID int64, threadID string) error {
	return s.App.DB.DeleteDraft(context.Background(), accountID, threadID)
}
