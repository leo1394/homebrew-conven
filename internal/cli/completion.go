package cli

import "fmt"

func Completion(shell string) (string, error) {
	switch shell {
	case "bash":
		return `_loom() {
    local cur subcommand action options i source_set
    cur="${COMP_WORDS[COMP_CWORD]}"
    if [ "$COMP_CWORD" -eq 1 ]; then
        COMPREPLY=( $(compgen -W "init services config policy doctor help version" -- "$cur") )
        return
    fi
    subcommand="${COMP_WORDS[1]}"
    if [ "$subcommand" = "services" ]; then
        action="${COMP_WORDS[2]}"
        case "$action" in
            --list|--status)
                options="--help"
                ;;
            --registry)
                options="--prune --help"
                ;;
            --logs)
                options="--tail --help"
                ;;
            --start)
                options="--env --dev --test --kubeconfig --context --namespace --tail --dry-run --skip-build --skip-verify --help"
                ;;
            --restart)
                options="--tail --skip-build --skip-verify --help"
                ;;
            --stop)
                options="--all --force --help"
                ;;
            --stop-all)
                options="--force --help"
                ;;
            *)
                if [ "$COMP_CWORD" -eq 2 ]; then
                    options="--list --registry --status --logs --start --restart --stop --stop-all --help"
                else
                    options=""
                fi
                ;;
        esac
        COMPREPLY=( $(compgen -W "$options" -- "$cur") )
        return
    fi
    if [ "$subcommand" = "policy" ]; then
        action="${COMP_WORDS[2]}"
        if [ "$COMP_CWORD" -eq 2 ]; then
            options="--edit --import --reset --help"
        else
            case "$action" in
                --edit|--reset)
                    options="--help"
                    ;;
                --import)
                    options="--edit --help"
                    source_set=0
                    for ((i=3; i<COMP_CWORD; i++)); do
                        if [ "${COMP_WORDS[i]}" = "--edit" ]; then
                            options="--help"
                        elif [[ "${COMP_WORDS[i]}" != -* ]]; then
                            source_set=1
                        fi
                    done
                    COMPREPLY=( $(compgen -W "$options" -- "$cur") )
                    if [ "$source_set" -eq 0 ] && [[ "$cur" != -* ]]; then
                        COMPREPLY+=( $(compgen -f -- "$cur") )
                    fi
                    return
                    ;;
                *)
                    options=""
                    ;;
            esac
        fi
        COMPREPLY=( $(compgen -W "$options" -- "$cur") )
        return
    fi
    case "$subcommand" in
        init)
            options="--help"
            ;;
        config)
            options="--global --list --unset --help"
            ;;
        doctor)
            options="--env --dev --test --kubeconfig --context --namespace --help"
            ;;
        help|version)
            options=""
            ;;
        *)
            options=""
            ;;
    esac
    COMPREPLY=( $(compgen -W "$options" -- "$cur") )
}
complete -F _loom loom
`, nil
	case "zsh":
		return `#compdef loom

_loom() {
    local -a commands
    local action
    commands=(
        'init:initialize a Loom workspace'
        'services:manage workspace services'
        'config:read or write Loom configuration'
        'policy:edit, import, or rebuild the workspace policy manifest'
        'doctor:validate workspace and connection configuration'
        'help:show loom usage'
        'version:show loom version'
    )
    if (( CURRENT == 2 )); then
        _describe 'command' commands
        return
    fi
    case $words[2] in
        services)
            action=''
            if (( CURRENT > 3 )); then
                action=$words[3]
                words=($words[1] $words[4,-1])
                (( CURRENT -= 2 ))
            fi
            case $action in
                --list|--status)
                    _arguments \
                        '--help[show command help]'
                    ;;
                --registry)
                    _arguments \
                        '--prune[remove missing direct-child repository services]' \
                        '--help[show command help]'
                    ;;
                --logs)
                    _arguments \
                        '--tail[tail aggregated logs]' \
                        '--help[show command help]' \
                        '*:service:'
                    ;;
                --start)
                    _arguments \
                        '--env[environment profile]:environment:' \
                        '--dev[use the dev environment profile]' \
                        '--test[use the test environment profile]' \
                        '--kubeconfig[kubeconfig path]:file:_files' \
                        '--context[kubeconfig context]:context:' \
                        '--namespace[Kubernetes namespace]:namespace:' \
                        '--tail[tail aggregated logs]' \
                        '--dry-run[show the startup plan]' \
                        '--skip-build[skip build when artifacts are reusable]' \
                        '--skip-verify[skip health checks]' \
                        '--help[show command help]' \
                        '*:service:'
                    ;;
                --restart)
                    _arguments \
                        '--tail[tail aggregated logs]' \
                        '--skip-build[skip build when artifacts are reusable]' \
                        '--skip-verify[skip health checks]' \
                        '--help[show command help]' \
                        '*:service:'
                    ;;
                --stop)
                    _arguments \
                        '--all[stop every service and release the workspace connection]' \
                        '--force[bypass identity checks and recover saved process groups]' \
                        '--help[show command help]' \
                        '*:service:'
                    ;;
                --stop-all)
                    _arguments \
                        '--force[bypass identity checks and recover saved process groups]' \
                        '--help[show command help]'
                    ;;
                *)
                    if (( CURRENT == 3 )); then
                        _arguments \
                            '--list[list services declared by the workspace]' \
                            '--registry[update services from child repositories]' \
                            '--status[show current local service state]' \
                            '--logs[show or tail current session logs]' \
                            '--start[select and start local services]' \
                            '--restart[restart changed local services]' \
                            '--stop[stop selected local services]' \
                            '--stop-all[stop all services and release the workspace connection]' \
                            '--help[show command help]'
                    else
                        _message 'unknown loom services action'
                    fi
                    ;;
            esac
            ;;
        init)
            _arguments \
                '--help[show command help]'
            ;;
        config)
            _arguments \
                '--global[use the current user global config]' \
                '--list[list configuration values]' \
                '--unset[remove one configuration value]' \
                '--help[show command help]' \
                '*:configuration key or value:'
            ;;
        policy)
            action=''
            if (( CURRENT > 3 )); then
                action=$words[3]
                words=($words[1] $words[4,-1])
                (( CURRENT -= 2 ))
            fi
            case $action in
                --edit|--reset)
                    _arguments \
                        '--help[show command help]'
                    ;;
                --import)
                    _arguments \
                        '--edit[edit the private import draft before publication]' \
                        '--help[show command help]' \
                        '1:yaml file:_files'
                    ;;
                *)
                    if (( CURRENT == 3 )); then
                        _arguments \
                            '--edit[edit a validated temporary manifest copy]' \
                            '--import[import a local YAML file as the entire manifest]' \
                            '--reset[destructively reset the manifest to scanned facts]' \
                            '--help[show command help]'
                    else
                        _message 'unknown loom policy action'
                    fi
                    ;;
            esac
            ;;
        doctor)
            _arguments \
                '--env[environment profile]:environment:' \
                '--dev[use the dev environment profile]' \
                '--test[use the test environment profile]' \
                '--kubeconfig[kubeconfig path]:file:_files' \
                '--context[kubeconfig context]:context:' \
                '--namespace[Kubernetes namespace]:namespace:' \
                '--help[show command help]'
            ;;
        help|version)
            ;;
        *)
            _message 'unknown loom command'
            ;;
    esac
}

compdef _loom loom
`, nil
	case "fish":
		return `function __loom_using_subcommand
    set -l tokens (commandline -opc)
    test (count $tokens) -ge 2; or return 1
    contains -- $tokens[2] $argv
end

function __loom_services_action
    set -l tokens (commandline -opc)
    test (count $tokens) -ge 3; or return 1
    test "$tokens[2]" = services; or return 1
    test "$tokens[3]" = "$argv[1]"
end

function __loom_services_without_action
    set -l tokens (commandline -opc)
    test (count $tokens) -eq 2; or return 1
    test "$tokens[2]" = services; or return 1
end

function __loom_policy_without_action
    set -l tokens (commandline -opc)
    test (count $tokens) -eq 2; or return 1
    test "$tokens[2]" = policy; or return 1
end

function __loom_policy_action
    set -l tokens (commandline -opc)
    test (count $tokens) -ge 3; or return 1
    test "$tokens[2]" = policy; or return 1
    test "$tokens[3]" = "$argv[1]"
end

function __loom_policy_action_without_edit
    set -l tokens (commandline -opc)
    __loom_policy_action "$argv[1]"; or return 1
    not contains -- --edit $tokens[4..-1]
end

function __loom_policy_import_without_source
    set -l tokens (commandline -opc)
    __loom_policy_action --import; or return 1
    for token in $tokens[4..-1]
        string match -q -- '-*' "$token"; or return 1
    end
    return 0
end

complete -c loom -f -n '__fish_use_subcommand' -a init -d 'Initialize a Loom workspace'
complete -c loom -f -n '__fish_use_subcommand' -a services -d 'Manage workspace services'
complete -c loom -f -n '__fish_use_subcommand' -a config -d 'Read or write Loom configuration'
complete -c loom -f -n '__fish_use_subcommand' -a policy -d 'Edit, import, or rebuild the workspace policy manifest'
complete -c loom -f -n '__fish_use_subcommand' -a doctor -d 'Validate workspace configuration'
complete -c loom -f -n '__fish_use_subcommand' -a help -d 'Show loom usage'
complete -c loom -f -n '__fish_use_subcommand' -a version -d 'Show loom version'
complete -c loom -n '__loom_using_subcommand init services config policy doctor' -s h -l help -d 'Show command help'
complete -c loom -n '__loom_using_subcommand config' -l global -d 'Use the current user global config'
complete -c loom -n '__loom_using_subcommand config' -l list -d 'List configuration values'
complete -c loom -n '__loom_using_subcommand config' -l unset -d 'Remove one configuration value'
complete -c loom -n '__loom_using_subcommand policy; and __loom_policy_without_action' -l edit -d 'Edit a validated temporary manifest copy'
complete -c loom -n '__loom_using_subcommand policy; and __loom_policy_without_action' -l import -d 'Import a local YAML file as the entire manifest'
complete -c loom -n '__loom_using_subcommand policy; and __loom_policy_without_action' -l reset -d 'Destructively reset the manifest to scanned facts'
complete -c loom -n '__loom_policy_action_without_edit --import' -l edit -d 'Edit the private import draft before publication'
complete -c loom -n '__loom_policy_import_without_source' -F
complete -c loom -n '__loom_using_subcommand doctor' -l env -r -d 'Environment profile'
complete -c loom -n '__loom_using_subcommand doctor' -l dev -d 'Use the dev environment profile'
complete -c loom -n '__loom_using_subcommand doctor' -l test -d 'Use the test environment profile'
complete -c loom -n '__loom_using_subcommand doctor' -l kubeconfig -r -d 'Kubeconfig path'
complete -c loom -n '__loom_using_subcommand doctor' -l context -r -d 'Kubeconfig context'
complete -c loom -n '__loom_using_subcommand doctor' -l namespace -r -d 'Kubernetes namespace'
complete -c loom -n '__loom_using_subcommand services; and __loom_services_without_action' -l list -d 'List services declared by the workspace'
complete -c loom -n '__loom_using_subcommand services; and __loom_services_without_action' -l registry -d 'Update services from child repositories'
complete -c loom -n '__loom_using_subcommand services; and __loom_services_without_action' -l status -d 'Show current local service state'
complete -c loom -n '__loom_using_subcommand services; and __loom_services_without_action' -l logs -d 'Show or tail current session logs'
complete -c loom -n '__loom_using_subcommand services; and __loom_services_without_action' -l start -d 'Select and start local services'
complete -c loom -n '__loom_using_subcommand services; and __loom_services_without_action' -l restart -d 'Restart changed local services'
complete -c loom -n '__loom_using_subcommand services; and __loom_services_without_action' -l stop -d 'Stop selected local services'
complete -c loom -n '__loom_using_subcommand services; and __loom_services_without_action' -l stop-all -d 'Stop all services and release the workspace connection'
complete -c loom -n '__loom_services_action --registry' -l prune -d 'Remove missing direct-child repository services'
complete -c loom -n '__loom_services_action --logs' -l tail -d 'Tail aggregated logs'
complete -c loom -n '__loom_services_action --start' -l env -r -d 'Environment profile'
complete -c loom -n '__loom_services_action --start' -l dev -d 'Use the dev environment profile'
complete -c loom -n '__loom_services_action --start' -l test -d 'Use the test environment profile'
complete -c loom -n '__loom_services_action --start' -l kubeconfig -r -d 'Kubeconfig path'
complete -c loom -n '__loom_services_action --start' -l context -r -d 'Kubeconfig context'
complete -c loom -n '__loom_services_action --start' -l namespace -r -d 'Kubernetes namespace'
complete -c loom -n '__loom_services_action --start' -l tail -d 'Tail aggregated logs'
complete -c loom -n '__loom_services_action --start' -l dry-run -d 'Show the startup plan'
complete -c loom -n '__loom_services_action --start' -l skip-build -d 'Skip build when artifacts are reusable'
complete -c loom -n '__loom_services_action --start' -l skip-verify -d 'Skip health checks'
complete -c loom -n '__loom_services_action --restart' -l tail -d 'Tail aggregated logs'
complete -c loom -n '__loom_services_action --restart' -l skip-build -d 'Skip build when artifacts are reusable'
complete -c loom -n '__loom_services_action --restart' -l skip-verify -d 'Skip health checks'
complete -c loom -n '__loom_services_action --stop' -l all -d 'Stop every service and release the workspace connection'
complete -c loom -n '__loom_services_action --stop' -l force -d 'Bypass identity checks and recover saved process groups'
complete -c loom -n '__loom_services_action --stop-all' -l force -d 'Bypass identity checks and recover saved process groups'
`, nil
	default:
		return "", fmt.Errorf("unsupported completion shell %q", shell)
	}
}
