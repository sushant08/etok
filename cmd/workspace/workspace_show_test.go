package workspace

import (
	"bytes"
	"context"
	"testing"

	cmdutil "github.com/leg100/etok/cmd/util"
	"github.com/leg100/etok/pkg/env"
	"github.com/leg100/etok/pkg/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceShow(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  *env.Env
		out  string
		err  bool
	}{
		{
			name: "WithEnvironmentFile",
			args: []string{"show"},
			env:  &env.Env{Namespace: "default", Workspace: "workspace-1"},
			out:  "default/workspace-1\n",
		},
		{
			name: "WithoutEnvironmentFile",
			args: []string{"show"},
			out:  "default/default\n",
		},
	}

	for _, tt := range tests {
		testutil.Run(t, tt.name, func(t *testutil.T) {
			path := t.NewTempDir().Chdir().Root()

			// Write .terraform/environment
			if tt.env != nil {
				require.NoError(t, tt.env.Write(path))
			}

			out := new(bytes.Buffer)

			f := cmdutil.NewFakeFactory(out)

			cmd := showCmd(f)
			cmd.SetOut(f.Out)
			cmd.SetArgs(tt.args)

			t.CheckError(tt.err, cmd.ExecuteContext(context.Background()))

			assert.Equal(t, tt.out, out.String())
		})
	}
}
