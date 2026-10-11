package app

import (
	"context"
	"fmt"
	"github.com/wen/opentalon/internal/experience"
)

func pinExperience(ctx context.Context, cfg *Config, newRun bool) error {
	if cfg.Experiences == nil {
		if cfg.RunConfig.ExperienceRelease != "" {
			return fmt.Errorf("pinned experience release requires retrieval service")
		}
		return nil
	}
	if newRun && cfg.RunConfig.ExperienceRelease == "" {
		id, e := cfg.Experiences.Active(ctx)
		if e != nil {
			return e
		}
		cfg.RunConfig.ExperienceRelease = id
	}
	if cfg.RunConfig.ExperienceRelease == "" {
		return nil
	}
	if cfg.RunConfig.ExperienceProfile != "" && cfg.RunConfig.ExperienceProfile != cfg.Experiences.Index.Profile() {
		return fmt.Errorf("pinned experience profile changed")
	}
	_, e := cfg.Experiences.Open(ctx, cfg.RunConfig.ExperienceRelease, cfg.ExperienceValidation)
	if e != nil {
		return e
	}
	cfg.RunConfig.ExperienceProfile = cfg.Experiences.Index.Profile()
	return nil
}
func experienceReader(ctx context.Context, cfg Config) (experience.Reader, error) {
	if e := pinExperience(ctx, &cfg, false); e != nil {
		return nil, e
	}
	if cfg.RunConfig.ExperienceRelease == "" {
		return nil, nil
	}
	return cfg.Experiences.Open(ctx, cfg.RunConfig.ExperienceRelease, cfg.ExperienceValidation)
}
