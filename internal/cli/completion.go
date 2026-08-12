package cli

import "fmt"

func Completion(shell string) (string, error) {
	switch shell {
	case "bash":
		return `_conven() {
    local cur subcommand action options i source_set candidate command_index action_index
    cur="${COMP_WORDS[COMP_CWORD]}"
    command_index=0
    for ((i=1; i<COMP_CWORD; )); do
        if [ "${COMP_WORDS[i]}" = "-C" ]; then
            if [ $((i + 1)) -eq "$COMP_CWORD" ]; then
                compopt -o filenames 2>/dev/null
                COMPREPLY=()
                while IFS= read -r candidate; do
                    COMPREPLY+=("$candidate")
                done < <(compgen -d -- "$cur")
                return
            fi
            ((i += 2))
            continue
        fi
        command_index=$i
        break
    done
    if [ "$command_index" -eq 0 ]; then
        COMPREPLY=( $(compgen -W "-C init services config policy plugins doctor help version" -- "$cur") )
        return
    fi
    subcommand="${COMP_WORDS[command_index]}"
    action_index=$((command_index + 1))
    if [ "$subcommand" = "help" ]; then
        if [ "$COMP_CWORD" -eq "$action_index" ]; then
            COMPREPLY=( $(compgen -W "init services config policy plugins doctor help version" -- "$cur") )
        else
            COMPREPLY=()
        fi
        return
    fi
    if [ "$subcommand" = "services" ]; then
        action="${COMP_WORDS[action_index]}"
        case "$action" in
            --list|--status|--dashboard|--cleanup)
                options="--help"
                ;;
            --registry)
                options="--prune --help"
                ;;
            --logs)
                options="--tail --dashboard --help"
                ;;
            --start)
                options="--env --dev --test --kubeconfig --context --namespace --tail --dry-run --skip-build --skip-verify --help"
                ;;
            --restart)
                options="--tail --dashboard --skip-build --skip-verify --help"
                ;;
            --stop)
                options="--all --force --help"
                ;;
            --stop-all)
                options="--force --help"
                ;;
            *)
                if [ "$COMP_CWORD" -eq "$action_index" ]; then
                    options="--list --registry --status --logs --dashboard --start --restart --stop --stop-all --cleanup --help"
                else
                    options=""
                fi
                ;;
        esac
        COMPREPLY=( $(compgen -W "$options" -- "$cur") )
        return
    fi
    if [ "$subcommand" = "policy" ]; then
        action="${COMP_WORDS[action_index]}"
        if [ "$COMP_CWORD" -eq "$action_index" ]; then
            options="--edit --import --reset --help"
        else
            case "$action" in
                --edit|--reset)
                    options="--help"
                    ;;
                --import)
                    options="--edit --help"
                    source_set=0
                    for ((i=action_index + 1; i<COMP_CWORD; i++)); do
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
    if [ "$subcommand" = "plugins" ]; then
        action="${COMP_WORDS[action_index]}"
        if [ "$COMP_CWORD" -eq "$action_index" ]; then
            options="--install --list --remove --run --help"
        elif [ "$action" = "--install" ] && [ "$COMP_CWORD" -eq $((action_index + 1)) ]; then
            compopt -o filenames 2>/dev/null
            COMPREPLY=()
            while IFS= read -r candidate; do
                if [ -d "$candidate" ] || [[ "$candidate" = *.py ]]; then
                    COMPREPLY+=("$candidate")
                fi
            done < <(compgen -f -- "$cur")
            return
        else
            options=""
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
complete -F _conven conven
`, nil
	case "zsh":
		return `#compdef conven

_conven() {
    local -a commands root_candidates
    local action scan
    commands=(
        'init:initialize a Conven workspace'
        'services:manage workspace services'
        'config:read or write Conven configuration'
        'policy:edit, import, or rebuild the workspace policy manifest'
        'plugins:install, list, remove, or run Conven plugins'
        'doctor:validate workspace and connection configuration'
        'help:show conven usage'
        'version:show conven version'
    )
    root_candidates=(
        '-C:run as if conven was started in a different directory'
        $commands
    )
    scan=2
    while (( scan < CURRENT )) && [[ $words[scan] == -C ]]; do
        if (( scan + 1 == CURRENT )); then
            _directories
            return
        fi
        (( scan += 2 ))
    done
    if (( scan == CURRENT )); then
        _describe 'command or global option' root_candidates
        return
    fi
    if (( scan > 2 )); then
        words=($words[1] $words[scan,-1])
        (( CURRENT -= scan - 2 ))
    fi
    if (( CURRENT == 2 )); then
        _describe 'command or global option' root_candidates
        return
    fi
    case $words[2] in
        help)
            _arguments \
                '1:command:(init services config policy plugins doctor help version)'
            ;;
        services)
            action=''
            if (( CURRENT > 3 )); then
                action=$words[3]
                words=($words[1] $words[4,-1])
                (( CURRENT -= 2 ))
            fi
            case $action in
                --list|--status|--cleanup)
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
                        '--tail[stream aggregated logs as plain text]' \
                        '--dashboard[open the interactive log dashboard]' \
                        '--help[show command help]' \
                        '*:service:'
                    ;;
                --dashboard)
                    _arguments \
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
                        '--tail[stream aggregated logs as plain text]' \
                        '--dry-run[show the startup plan]' \
                        '--skip-build[skip build when artifacts are reusable]' \
                        '--skip-verify[skip health checks]' \
                        '--help[show command help]' \
                        '*:service:'
                    ;;
                --restart)
                    _arguments \
                        '--tail[stream aggregated logs as plain text]' \
                        '--dashboard[open the interactive log dashboard]' \
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
                            '--logs[show or stream current session logs]' \
                            '--dashboard[open the interactive log dashboard]' \
                            '--start[select and start local services]' \
                            '--restart[restart changed local services]' \
                            '--stop[stop selected local services]' \
                            '--stop-all[stop all services and release the workspace connection]' \
                            '--cleanup[remove saved build artifacts and service logs]' \
                            '--help[show command help]'
                    else
                        _message 'unknown conven services action'
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
                        _message 'unknown conven policy action'
                    fi
                    ;;
            esac
            ;;
        plugins)
            action=''
            if (( CURRENT > 3 )); then
                action=$words[3]
                words=($words[1] $words[4,-1])
                (( CURRENT -= 2 ))
            fi
            case $action in
                --install)
                    _arguments \
                        '1:Python plugin file:_files -g "*.py"'
                    ;;
                --list)
                    _message 'this plugin action does not accept arguments'
                    ;;
                --remove)
                    _arguments \
                        '1:plugin:'
                    ;;
                --run)
                    _arguments \
                        '1:plugin:' \
                        '*:plugin argument:'
                    ;;
                *)
                    if (( CURRENT == 3 )); then
                        _arguments \
                            '--install[install a Python plugin]:Python plugin file:_files -g "*.py"' \
                            '--list[list installed plugins]' \
                            '--remove[remove an installed plugin]:plugin:' \
                            '--run[run an installed plugin]:plugin:' \
                            '--help[show command help]'
                    else
                        _message 'unknown conven plugins action'
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
        version)
            ;;
        *)
            _message 'unknown conven command'
            ;;
    esac
}

compdef _conven conven
`, nil
	case "fish":
		return `function __conven_command_tokens
    set -l tokens (commandline -opc)
    set -l normalized $tokens[1]
    set -l index 2
    while test $index -le (count $tokens)
        if test "$tokens[$index]" = -C
            set index (math $index + 2)
            continue
        end
        set -a normalized $tokens[$index..-1]
        break
    end
    printf '%s\n' $normalized
end

function __conven_global_context
    set -l tokens (commandline -opc)
    set -l index 2
    while test $index -le (count $tokens)
        test "$tokens[$index]" = -C; or return 1
        set index (math $index + 2)
    end
    return 0
end

function __conven_without_command
    set -l raw_tokens (commandline -opc)
    if test (count $raw_tokens) -ge 2; and test "$raw_tokens[-1]" = -C
        return 1
    end
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -eq 1
end

function __conven_using_subcommand
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -ge 2; or return 1
    contains -- $tokens[2] $argv
end

function __conven_help_without_command
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -eq 2; or return 1
    test "$tokens[2]" = help
end

function __conven_services_action
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -ge 3; or return 1
    test "$tokens[2]" = services; or return 1
    test "$tokens[3]" = "$argv[1]"
end

function __conven_services_without_action
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -eq 2; or return 1
    test "$tokens[2]" = services; or return 1
end

function __conven_policy_without_action
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -eq 2; or return 1
    test "$tokens[2]" = policy; or return 1
end

function __conven_plugins_without_action
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -eq 2; or return 1
    test "$tokens[2]" = plugins; or return 1
end

function __conven_policy_action
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -ge 3; or return 1
    test "$tokens[2]" = policy; or return 1
    test "$tokens[3]" = "$argv[1]"
end

function __conven_policy_action_without_edit
    set -l tokens (__conven_command_tokens)
    __conven_policy_action "$argv[1]"; or return 1
    not contains -- --edit $tokens[4..-1]
end

function __conven_policy_import_without_source
    set -l tokens (__conven_command_tokens)
    __conven_policy_action --import; or return 1
    for token in $tokens[4..-1]
        string match -q -- '-*' "$token"; or return 1
    end
    return 0
end

complete -c conven -f -n '__conven_global_context' -s C -r -a '(__fish_complete_directories)' -d 'Run as if conven was started in another directory'
complete -c conven -f -n '__conven_without_command' -a init -d 'Initialize a Conven workspace'
complete -c conven -f -n '__conven_without_command' -a services -d 'Manage workspace services'
complete -c conven -f -n '__conven_without_command' -a config -d 'Read or write Conven configuration'
complete -c conven -f -n '__conven_without_command' -a policy -d 'Edit, import, or rebuild the workspace policy manifest'
complete -c conven -f -n '__conven_without_command' -a plugins -d 'Install, list, remove, or run Conven plugins'
complete -c conven -f -n '__conven_without_command' -a doctor -d 'Validate workspace configuration'
complete -c conven -f -n '__conven_without_command' -a help -d 'Show conven usage'
complete -c conven -f -n '__conven_without_command' -a version -d 'Show conven version'
complete -c conven -f -n '__conven_using_subcommand help; and __conven_help_without_command' -a 'init services config policy plugins doctor help version' -d 'Show detailed command help'
complete -c conven -n '__conven_using_subcommand init services config policy plugins doctor' -s h -l help -d 'Show command help'
complete -c conven -n '__conven_using_subcommand config' -l global -d 'Use the current user global config'
complete -c conven -n '__conven_using_subcommand config' -l list -d 'List configuration values'
complete -c conven -n '__conven_using_subcommand config' -l unset -d 'Remove one configuration value'
complete -c conven -n '__conven_using_subcommand policy; and __conven_policy_without_action' -l edit -d 'Edit a validated temporary manifest copy'
complete -c conven -n '__conven_using_subcommand policy; and __conven_policy_without_action' -l import -d 'Import a local YAML file as the entire manifest'
complete -c conven -n '__conven_using_subcommand policy; and __conven_policy_without_action' -l reset -d 'Destructively reset the manifest to scanned facts'
complete -c conven -n '__conven_policy_action_without_edit --import' -l edit -d 'Edit the private import draft before publication'
complete -c conven -n '__conven_policy_import_without_source' -F
complete -c conven -n '__conven_using_subcommand plugins; and __conven_plugins_without_action' -l install -r -a '(__fish_complete_suffix .py)' -d 'Install a Python plugin'
complete -c conven -n '__conven_using_subcommand plugins; and __conven_plugins_without_action' -l list -d 'List installed plugins'
complete -c conven -n '__conven_using_subcommand plugins; and __conven_plugins_without_action' -l remove -r -d 'Remove an installed plugin'
complete -c conven -n '__conven_using_subcommand plugins; and __conven_plugins_without_action' -l run -r -d 'Run an installed plugin'
complete -c conven -n '__conven_using_subcommand doctor' -l env -r -d 'Environment profile'
complete -c conven -n '__conven_using_subcommand doctor' -l dev -d 'Use the dev environment profile'
complete -c conven -n '__conven_using_subcommand doctor' -l test -d 'Use the test environment profile'
complete -c conven -n '__conven_using_subcommand doctor' -l kubeconfig -r -d 'Kubeconfig path'
complete -c conven -n '__conven_using_subcommand doctor' -l context -r -d 'Kubeconfig context'
complete -c conven -n '__conven_using_subcommand doctor' -l namespace -r -d 'Kubernetes namespace'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l list -d 'List services declared by the workspace'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l registry -d 'Update services from child repositories'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l status -d 'Show current local service state'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l logs -d 'Show or stream current session logs'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l dashboard -d 'Open the interactive log dashboard'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l start -d 'Select and start local services'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l restart -d 'Restart changed local services'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l stop -d 'Stop selected local services'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l stop-all -d 'Stop all services and release the workspace connection'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l cleanup -d 'Remove saved build artifacts and service logs'
complete -c conven -n '__conven_services_action --registry' -l prune -d 'Remove missing direct-child repository services'
complete -c conven -n '__conven_services_action --logs' -l tail -d 'Stream aggregated logs as plain text'
complete -c conven -n '__conven_services_action --logs' -l dashboard -d 'Open the interactive log dashboard'
complete -c conven -n '__conven_services_action --start' -l env -r -d 'Environment profile'
complete -c conven -n '__conven_services_action --start' -l dev -d 'Use the dev environment profile'
complete -c conven -n '__conven_services_action --start' -l test -d 'Use the test environment profile'
complete -c conven -n '__conven_services_action --start' -l kubeconfig -r -d 'Kubeconfig path'
complete -c conven -n '__conven_services_action --start' -l context -r -d 'Kubeconfig context'
complete -c conven -n '__conven_services_action --start' -l namespace -r -d 'Kubernetes namespace'
complete -c conven -n '__conven_services_action --start' -l tail -d 'Stream aggregated logs as plain text'
complete -c conven -n '__conven_services_action --start' -l dry-run -d 'Show the startup plan'
complete -c conven -n '__conven_services_action --start' -l skip-build -d 'Skip build when artifacts are reusable'
complete -c conven -n '__conven_services_action --start' -l skip-verify -d 'Skip health checks'
complete -c conven -n '__conven_services_action --restart' -l tail -d 'Stream aggregated logs as plain text'
complete -c conven -n '__conven_services_action --restart' -l dashboard -d 'Open the interactive log dashboard'
complete -c conven -n '__conven_services_action --restart' -l skip-build -d 'Skip build when artifacts are reusable'
complete -c conven -n '__conven_services_action --restart' -l skip-verify -d 'Skip health checks'
complete -c conven -n '__conven_services_action --stop' -l all -d 'Stop every service and release the workspace connection'
complete -c conven -n '__conven_services_action --stop' -l force -d 'Bypass identity checks and recover saved process groups'
complete -c conven -n '__conven_services_action --stop-all' -l force -d 'Bypass identity checks and recover saved process groups'
`, nil
	default:
		return "", fmt.Errorf("unsupported completion shell %q", shell)
	}
}
