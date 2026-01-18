package config

import (
	"os/exec"

	"github.com/teacat/chaturbate-dvr/entity"
	"github.com/urfave/cli/v2"
)

// HasFFmpeg checks if ffmpeg is installed and available in PATH.
func HasFFmpeg() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// New initializes a new Config struct with values from the CLI context.
func New(c *cli.Context) (*entity.Config, error) {
	// Auto-enable compress if ffmpeg is available and user didn't explicitly set --compress=false
	compress := c.Bool("compress")
	if !c.IsSet("compress") && HasFFmpeg() {
		compress = true
	}

	return &entity.Config{
		Version:       c.App.Version,
		Username:      c.String("username"),
		AdminUsername: c.String("admin-username"),
		AdminPassword: c.String("admin-password"),
		Framerate:     c.Int("framerate"),
		Resolution:    c.Int("resolution"),
		Pattern:       c.String("pattern"),
		MaxDuration:   c.Int("max-duration"),
		MaxFilesize:   c.Int("max-filesize"),
		Compress:      compress,
		Port:          c.String("port"),
		Interval:      c.Int("interval"),
		Cookies:       c.String("cookies"),
		UserAgent:     c.String("user-agent"),
		Domain:        c.String("domain"),
	}, nil
}
