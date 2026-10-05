package workflow

import "fmt"

// ResumeInvestigation is called only after the controller has reconciled all
// submitted operations. Old intents/results remain historical context; no old
// action is authorized by this transition.
func (w *IncidentWorkflow) ResumeInvestigation() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state == StateProtected {
		_, err := w.applyLocked(Event{Type: EventStartInvestigation, Actor: ActorController, Reason: "resume run with fresh observations"})
		return err
	}
	if _, ok := transitionRules[w.state][EventRunResumed]; !ok {
		return fmt.Errorf("%w: cannot resume %s", ErrInvalidTransition, w.state)
	}
	if w.agentResumesUsed >= w.limits.MaxAgentResumes {
		_, err := w.applyLocked(Event{Type: EventEscalated, Actor: ActorController, Reason: "run resume investigation budget exhausted"})
		return err
	}
	_, err := w.applyLocked(Event{Type: EventRunResumed, Actor: ActorController, Reason: "operations reconciled; investigate current environment before any further repair"})
	return err
}
