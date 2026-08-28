package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/amagioss/opentams/internal/tamsctl"
)

func newFlowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flow",
		Short: "Create, update, get, and delete flows",
	}
	cmd.AddCommand(
		newFlowCreateCmd(),
		newFlowUpdateCmd(),
		newFlowGetCmd(),
		newFlowDeleteCmd(),
	)
	return cmd
}

func newFlowCreateCmd() *cobra.Command {
	var (
		p         tamsctl.FlowCreateParams
		frameRate string
		vfr       bool
		newSource bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a video flow",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateUUID("id", p.ID); err != nil {
				return err
			}
			if err := validateUUID("source-id", p.SourceID); err != nil {
				return err
			}

			// Source: explicit --source-id, or --new-source to mint one.
			if newSource && p.SourceID != "" {
				return fmt.Errorf("--new-source and --source-id are mutually exclusive")
			}
			switch {
			case p.SourceID != "":
			case newSource:
				p.SourceID = uuid.NewString()
			default:
				return fmt.Errorf("provide --source-id, or --new-source to generate one")
			}

			switch {
			case vfr:
				if frameRate != "" {
					return fmt.Errorf("--frame-rate must not be set with --vfr")
				}
				p.VFR = true
			case frameRate != "":
				fr, err := parseFrameRate(frameRate)
				if err != nil {
					return err
				}
				p.FrameRate = fr
			default:
				return fmt.Errorf("a video flow needs a frame rate: pass --frame-rate (e.g. 25 or 30000/1001) or --vfr")
			}

			// Flow id is client-assigned; mint one if the user didn't supply it.
			genID := p.ID == ""
			if genID {
				p.ID = uuid.NewString()
			}
			if quiet, _ := cmd.Flags().GetBool("quiet"); !quiet {
				if genID {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "generated flow id: %s\n", p.ID)
				}
				if newSource {
					_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "generated source id: %s\n", p.SourceID)
				}
			}

			client, err := clientFromCmd(cmd)
			if err != nil {
				return err
			}
			raw, err := client.CreateFlow(cmd.Context(), p)
			if err != nil {
				return err
			}
			return emit(cmd, raw)
		},
	}
	f := cmd.Flags()
	f.StringVar(&p.ID, "id", "", "flow id (UUID; generated if omitted)")
	f.StringVar(&p.SourceID, "source-id", "", "source id (UUID; required unless --new-source)")
	f.BoolVar(&newSource, "new-source", false, "generate a new source id instead of passing --source-id")
	f.StringVar(&p.Codec, "codec", "", "codec mime type, e.g. video/mp4 (required)")
	f.StringVar(&p.Format, "format", "urn:x-nmos:format:video", "format URN")
	f.IntVar(&p.FrameWidth, "frame-width", 0, "frame width (required)")
	f.IntVar(&p.FrameHeight, "frame-height", 0, "frame height (required)")
	f.StringVar(&frameRate, "frame-rate", "", "fixed frame rate as N or N/D, e.g. 25 or 30000/1001")
	f.BoolVar(&vfr, "vfr", false, "variable frame rate (mutually exclusive with --frame-rate)")
	for _, name := range []string{"codec", "frame-width", "frame-height"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

// parseFrameRate parses "N" or "N/D" into a FrameRate (denominator defaults to 1).
func parseFrameRate(s string) (*tamsctl.FrameRate, error) {
	numStr, denStr, hasDen := strings.Cut(s, "/")
	num, err := strconv.Atoi(strings.TrimSpace(numStr))
	if err != nil || num <= 0 {
		return nil, fmt.Errorf("invalid --frame-rate %q: numerator must be a positive integer", s)
	}
	fr := &tamsctl.FrameRate{Numerator: num, Denominator: 1}
	if hasDen {
		den, derr := strconv.Atoi(strings.TrimSpace(denStr))
		if derr != nil || den <= 0 {
			return nil, fmt.Errorf("invalid --frame-rate %q: denominator must be a positive integer", s)
		}
		fr.Denominator = den
	}
	return fr, nil
}

func newFlowUpdateCmd() *cobra.Command {
	var (
		id             string
		tags           []string
		label          string
		readOnly       bool
		avgBitRate     int
		collectionFile string
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update flow properties via child endpoints",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateUUID("id", id); err != nil {
				return err
			}
			client, err := clientFromCmd(cmd)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			f := cmd.Flags()
			applied := 0

			if f.Changed("label") {
				if _, err := client.PutFlowLabel(ctx, id, label); err != nil {
					return err
				}
				applied++
			}
			if f.Changed("read-only") {
				if _, err := client.PutFlowReadOnly(ctx, id, readOnly); err != nil {
					return err
				}
				applied++
			}
			if f.Changed("avg-bit-rate") {
				if _, err := client.PutFlowAvgBitRate(ctx, id, avgBitRate); err != nil {
					return err
				}
				applied++
			}
			for _, tag := range tags {
				name, value, ok := strings.Cut(tag, "=")
				if !ok {
					return fmt.Errorf("invalid --tag %q: want name=value", tag)
				}
				if _, err := client.PutFlowTag(ctx, id, name, value); err != nil {
					return err
				}
				applied++
			}
			if collectionFile != "" {
				doc, err := os.ReadFile(collectionFile) //nolint:gosec // operator-supplied path.
				if err != nil {
					return fmt.Errorf("read flow-collection: %w", err)
				}
				if !json.Valid(doc) {
					return fmt.Errorf("flow-collection file is not valid JSON")
				}
				if _, err := client.PutFlowCollection(ctx, id, doc); err != nil {
					return err
				}
				applied++
			}
			if applied == 0 {
				return fmt.Errorf("nothing to update: set at least one of --tag/--label/--read-only/--avg-bit-rate/--flow-collection")
			}
			return outf(cmd, "flow %q updated (%d change(s))\n", id, applied)
		},
	}
	f := cmd.Flags()
	f.StringVar(&id, "id", "", "flow id (required)")
	f.StringArrayVar(&tags, "tag", nil, "tag as name=value (repeatable)")
	f.StringVar(&label, "label", "", "flow label")
	f.BoolVar(&readOnly, "read-only", false, "read-only flag")
	f.IntVar(&avgBitRate, "avg-bit-rate", 0, "average bit rate (1000 bits/s)")
	f.StringVar(&collectionFile, "flow-collection", "", "path to flow collection JSON file")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func newFlowGetCmd() *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Get a flow by id",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateUUID("id", id); err != nil {
				return err
			}
			client, err := clientFromCmd(cmd)
			if err != nil {
				return err
			}
			raw, err := client.GetFlow(cmd.Context(), id)
			if err != nil {
				return err
			}
			return emit(cmd, raw)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "flow id (required)")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func newFlowDeleteCmd() *cobra.Command {
	var id string
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a flow by id",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateUUID("id", id); err != nil {
				return err
			}
			client, err := clientFromCmd(cmd)
			if err != nil {
				return err
			}
			raw, err := client.DeleteFlow(cmd.Context(), id)
			if err != nil {
				return err
			}
			if len(raw) == 0 {
				return outf(cmd, "flow %q deleted\n", id)
			}
			return emit(cmd, raw)
		},
	}
	cmd.Flags().StringVar(&id, "id", "", "flow id (required)")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}
