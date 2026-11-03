package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/leo1394/homebrew-conven/internal/config"
)

// Resolve only selected Go build graphs on the host; never run service commands.
func PrepareGuardModules(ctx context.Context, workspace *WorkspaceData, environment string, selected []string, cache string) error {
	if err:=config.ValidateSelection(workspace.Manifest,selected); err!=nil { return err }
	for _,name:=range selected {
		service:=workspace.Manifest.Services[name]
		if len(service.Runner.Build)<2 || service.Runner.Build[0]!="go" || service.Runner.Build[1]!="build" { continue }
		args:=guardOfflineGoPrepare([]string{"go","mod","download"},service.Runner.Build)
		if len(args)<2 || args[1]!="list" { return fmt.Errorf("service %s: unsupported Go build dependency preparation",name) }
		dir:=service.Path; if !filepath.IsAbs(dir) { dir=filepath.Join(workspace.Root,dir) }
		workdir,err:=config.Expand(service.Runner.Workdir,config.ExpandContext{Workspace:workspace.Root,Service:name,ServiceDir:dir,Environment:environment,Manifest:workspace.Manifest}); if err!=nil { return err }
		if workdir=="" { workdir=dir }; if !filepath.IsAbs(workdir) { workdir=filepath.Join(dir,workdir) }
		for _,offline:=range []bool{false,true} {
			fmt.Fprintf(os.Stderr,"[Guard] Go module preparation · %s · offline=%t\n",name,offline)
			cmd:=exec.CommandContext(ctx,args[0],args[1:]...); cmd.Dir=workdir
			for _,entry:=range os.Environ() { if len(entry)>=10 && entry[:10]=="GOMODCACHE" { continue }; cmd.Env=append(cmd.Env,entry) }
			cmd.Env=append(cmd.Env,"GOOS=linux","GOARCH=arm64","CGO_ENABLED=0","GOMODCACHE="+cache)
			if offline { cmd.Env=append(cmd.Env,"GOPROXY=off") }
			cmd.Stdout=os.Stderr; cmd.Stderr=os.Stderr
			if err:=cmd.Run(); err!=nil { return fmt.Errorf("service %s: Go module preparation failed: %w",name,err) }
		}
	}
	return nil
}
