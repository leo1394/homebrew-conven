package cli

import "fmt"

func Completion(shell string) (string, error) {
	switch shell {
	case "bash":
		return `_conven() {
    local cur prev subcommand action options i source_set candidate command_index action_index candidate_kind candidate_scope
    local -a root_args
    cur="${COMP_WORDS[COMP_CWORD]}"
    prev=""
    if [ "$COMP_CWORD" -gt 0 ]; then
        prev="${COMP_WORDS[COMP_CWORD - 1]}"
    fi
    root_args=()
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
            root_args+=("-C" "${COMP_WORDS[i + 1]}")
            ((i += 2))
            continue
        fi
        command_index=$i
        break
    done
    if [ "$command_index" -eq 0 ]; then
        COMPREPLY=( $(compgen -W "-C init services config workspace plugins status doctor help version" -- "$cur") )
        return
    fi
    subcommand="${COMP_WORDS[command_index]}"
    action_index=$((command_index + 1))
    if [ "$subcommand" = "help" ]; then
        if [ "$COMP_CWORD" -eq "$action_index" ]; then
            COMPREPLY=( $(compgen -W "init services config workspace plugins status doctor help version" -- "$cur") )
        else
            COMPREPLY=()
        fi
        return
    fi
    if [ "$subcommand" = "services" ]; then
        action="${COMP_WORDS[action_index]}"
        candidate_kind=""
        case "$action" in
            --list|--status|--cleanup)
                options="--help"
                ;;
            --dashboard)
                options="--help"
                candidate_kind="services"
                ;;
            --registry)
                options="--prune --help"
                ;;
            --listen)
                options="--on --off --help"
                candidate_kind="services"
                ;;
            --logs)
                options="--tail --dashboard --help"
                candidate_kind="services"
                ;;
            --start)
                options="--env --dev --test --kubeconfig --context --namespace --tail --dry-run --with-dependencies --skip-build --skip-verify --help"
                candidate_kind="services"
                ;;
            --restart)
                options="--tail --dashboard --skip-build --skip-verify --help"
                candidate_kind="services"
                ;;
            --stop)
                options="--all --force --help"
                candidate_kind="services"
                ;;
            --stop-all)
                options="--force --help"
                ;;
            *)
                if [ "$COMP_CWORD" -eq "$action_index" ]; then
                    options="--list --registry --listen --status --logs --dashboard --start --restart --stop --stop-all --cleanup --help"
                else
                    options=""
                fi
                ;;
        esac
        if [ "$prev" = "--env" ]; then
            COMPREPLY=()
            while IFS= read -r candidate; do
                [[ "$candidate" == "$cur"* ]] && COMPREPLY+=("$candidate")
            done < <("${COMP_WORDS[0]}" "${root_args[@]}" __completion candidates environments 2>/dev/null)
            return
        fi
        if [ "$prev" = "--kubeconfig" ]; then
            compopt -o filenames 2>/dev/null
            COMPREPLY=()
            while IFS= read -r candidate; do
                COMPREPLY+=("$candidate")
            done < <(compgen -f -- "$cur")
            return
        fi
        if [ "$prev" = "--context" ] || [ "$prev" = "--namespace" ]; then
            COMPREPLY=()
            return
        fi
        COMPREPLY=( $(compgen -W "$options" -- "$cur") )
        if [ "$candidate_kind" = "services" ] && [[ "$cur" != -* ]]; then
            while IFS= read -r candidate; do
                [[ "$candidate" == "$cur"* ]] && COMPREPLY+=("$candidate")
            done < <("${COMP_WORDS[0]}" "${root_args[@]}" __completion candidates services 2>/dev/null)
        fi
        return
    fi
    if [ "$subcommand" = "workspace" ]; then
        action="${COMP_WORDS[action_index]}"
        if [ "$COMP_CWORD" -eq "$action_index" ]; then
            options="--edit --validate --migrate --import --reset --help"
        else
            case "$action" in
                --edit|--validate|--migrate|--reset)
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
                        while IFS= read -r candidate; do
                            COMPREPLY+=("$candidate")
                        done < <(compgen -f -- "$cur")
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
        candidate_scope=""
        if [ "$prev" = "--output" ]; then
            compopt -o filenames 2>/dev/null
            COMPREPLY=()
            while IFS= read -r candidate; do
                COMPREPLY+=("$candidate")
            done < <(compgen -f -- "$cur")
            return
        fi
        if [ "$COMP_CWORD" -eq "$action_index" ]; then
            options="--global --install --list --remove --run --help"
        elif [ "$action" = "--global" ]; then
            if [ "$COMP_CWORD" -eq $((action_index + 1)) ]; then
                options="--run"
            elif [ "${COMP_WORDS[action_index + 1]}" = "--run" ]; then
                if [ "$COMP_CWORD" -eq $((action_index + 2)) ]; then
                    options=""
                    candidate_scope="global"
                else
                    options="--output --disable-bindings"
                fi
            else
                options=""
            fi
        elif [ "$action" = "--install" ] && { [ "$COMP_CWORD" -eq $((action_index + 1)) ] || { [ "${COMP_WORDS[action_index + 1]}" = "--global" ] && [ "$COMP_CWORD" -eq $((action_index + 2)) ]; }; }; then
            compopt -o filenames 2>/dev/null
            COMPREPLY=()
            if [ "$COMP_CWORD" -eq $((action_index + 1)) ]; then
                COMPREPLY+=( $(compgen -W "--global" -- "$cur") )
            fi
            while IFS= read -r candidate; do
                if [ -d "$candidate" ] || [[ "$candidate" = *.py ]]; then
                    COMPREPLY+=("$candidate")
                fi
            done < <(compgen -f -- "$cur")
            return
        elif [ "$action" = "--run" ]; then
            if [ "$COMP_CWORD" -eq $((action_index + 1)) ]; then
                options="--global --output --disable-bindings"
                candidate_scope="workspace"
            elif [ "${COMP_WORDS[action_index + 1]}" = "--global" ] && [ "$COMP_CWORD" -eq $((action_index + 2)) ]; then
                options=""
                candidate_scope="global"
            else
                options="--output --disable-bindings"
            fi
        elif [ "$action" = "--remove" ]; then
            if [ "$COMP_CWORD" -eq $((action_index + 1)) ]; then
                options="--global"
                candidate_scope="workspace"
            elif [ "${COMP_WORDS[action_index + 1]}" = "--global" ] && [ "$COMP_CWORD" -eq $((action_index + 2)) ]; then
                options=""
                candidate_scope="global"
            else
                options=""
            fi
        elif [ "$action" = "--list" ]; then
            if [ "$COMP_CWORD" -eq $((action_index + 1)) ]; then
                options="--global"
            else
                options=""
            fi
        else
            options=""
        fi
        COMPREPLY=( $(compgen -W "$options" -- "$cur") )
        if [ -n "$candidate_scope" ] && [[ "$cur" != -* ]]; then
            while IFS= read -r candidate; do
                [[ "$candidate" == "$cur"* ]] && COMPREPLY+=("$candidate")
            done < <("${COMP_WORDS[0]}" "${root_args[@]}" __completion candidates plugins $([ "$candidate_scope" = "global" ] && printf global) 2>/dev/null)
        fi
        return
    fi
    case "$subcommand" in
        init)
            options="--local --help"
            ;;
        config)
            options="--global --list --unset --help"
            ;;
        status)
            options="--help"
            ;;
        doctor)
            options="--env --dev --test --kubeconfig --context --namespace --help"
            if [ "$prev" = "--env" ]; then
                COMPREPLY=()
                while IFS= read -r candidate; do
                    [[ "$candidate" == "$cur"* ]] && COMPREPLY+=("$candidate")
                done < <("${COMP_WORDS[0]}" "${root_args[@]}" __completion candidates environments 2>/dev/null)
                return
            fi
            if [ "$prev" = "--kubeconfig" ]; then
                compopt -o filenames 2>/dev/null
                COMPREPLY=()
                while IFS= read -r candidate; do
                    COMPREPLY+=("$candidate")
                done < <(compgen -f -- "$cur")
                return
            fi
            if [ "$prev" = "--context" ] || [ "$prev" = "--namespace" ]; then
                COMPREPLY=()
                return
            fi
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

_conven_service_names() {
    local -a values
    values=("${(@f)$("$conven_executable" "${conven_root_args[@]}" __completion candidates services 2>/dev/null)}")
    _describe 'service' values
}

_conven_environment_names() {
    local -a values
    values=("${(@f)$("$conven_executable" "${conven_root_args[@]}" __completion candidates environments 2>/dev/null)}")
    _describe 'environment' values
}

_conven_plugin_names() {
    local -a values
    values=("${(@f)$("$conven_executable" "${conven_root_args[@]}" __completion candidates plugins 2>/dev/null)}")
    _describe 'plugin' values
}

_conven_global_plugin_names() {
    local -a values
    values=("${(@f)$("$conven_executable" "${conven_root_args[@]}" __completion candidates plugins global 2>/dev/null)}")
    _describe 'global plugin' values
}

_conven() {
    local -a commands root_candidates plugin_scope conven_root_args
    local action scan conven_executable
    conven_executable=$words[1]
    conven_root_args=()
    commands=(
        'init:initialize a Conven workspace'
        'services:manage workspace services'
        'config:read or write Conven configuration'
        'workspace:edit, validate, migrate, import, or rebuild the workspace manifest'
        'plugins:install, list, remove, or run Conven plugins'
        'status:show the complete workspace and runtime status'
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
        conven_root_args+=(-C $words[scan+1])
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
                '1:command:(init services config workspace plugins status doctor help version)'
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
                --listen)
                    _arguments \
                        '--on[listen on all interfaces]' \
                        '--off[restore loopback-only listening]' \
                        '--help[show command help]' \
                        '*:service:_conven_service_names'
                    ;;
                --logs)
                    _arguments \
                        '--tail[stream aggregated logs as plain text]' \
                        '--dashboard[open the interactive log dashboard]' \
                        '--help[show command help]' \
                        '*:service:_conven_service_names'
                    ;;
                --dashboard)
                    _arguments \
                        '--help[show command help]' \
                        '*:service:_conven_service_names'
                    ;;
                --start)
                    _arguments \
                        '--env[environment profile]:environment:_conven_environment_names' \
                        '--dev[use the dev environment profile]' \
                        '--test[use the test environment profile]' \
                        '--kubeconfig[kubeconfig path]:file:_files' \
                        '--context[kubeconfig context]:context:' \
                        '--namespace[Kubernetes namespace]:namespace:' \
                        '--tail[stream aggregated logs as plain text]' \
                        '--dry-run[show the startup plan]' \
                        '--with-dependencies[also start transitive local service dependencies]' \
                        '--skip-build[skip build when artifacts are reusable]' \
                        '--skip-verify[skip health, listener, and registry verification]' \
                        '--help[show command help]' \
                        '*:service:_conven_service_names'
                    ;;
                --restart)
                    _arguments \
                        '--tail[stream aggregated logs as plain text]' \
                        '--dashboard[open the interactive log dashboard]' \
                        '--skip-build[skip build when artifacts are reusable]' \
                        '--skip-verify[skip health, listener, and registry verification]' \
                        '--help[show command help]' \
                        '*:service:_conven_service_names'
                    ;;
                --stop)
                    _arguments \
                        '--all[stop every service and release the workspace connection]' \
                        '--force[bypass identity checks and recover saved process groups]' \
                        '--help[show command help]' \
                        '*:service:_conven_service_names'
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
                            '--listen[change listener scope for selected services]' \
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
                '--local[initialize no-cluster local development]' \
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
        workspace)
            action=''
            if (( CURRENT > 3 )); then
                action=$words[3]
                words=($words[1] $words[4,-1])
                (( CURRENT -= 2 ))
            fi
            case $action in
                --edit|--validate|--migrate|--reset)
                    _arguments \
                        '--help[show command help]'
                    ;;
                --import)
                    _arguments \
                        '--edit[edit the private import draft before publication]' \
                        '--help[show command help]' \
                        '1::yaml file:_files'
                    ;;
                *)
                    if (( CURRENT == 3 )); then
                        _arguments \
                            '--edit[edit a validated temporary manifest copy]' \
                            '--validate[validate the current workspace manifest]' \
                            '--migrate[atomically migrate a stopped v1/v2 manifest to v3]' \
                            '--import[import a local YAML file as the entire manifest]' \
                            '--reset[destructively reset the manifest to scanned facts]' \
                            '--help[show command help]'
                    else
                        _message 'unknown conven workspace action'
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
            plugin_scope=()
            if (( CURRENT == 2 )) || [[ $words[2] == --global ]]; then
                plugin_scope=('--global[use the user-global plugin scope]')
            fi
            case $action in
                --install)
                    _arguments \
                        $plugin_scope \
                        '1:Python plugin file:_files -g "*.py"'
                    ;;
                --list)
                    _arguments \
                        $plugin_scope
                    ;;
                --remove)
                    _arguments \
                        $plugin_scope \
                        '1:plugin:_conven_plugin_names'
                    ;;
                --run)
                    if [[ $words[2] == --global ]]; then
                        words=($words[1] $words[3,-1])
                        (( CURRENT -= 1 ))
                        if (( CURRENT == 2 )); then
                            _arguments \
                                '1:global plugin:_conven_global_plugin_names'
                        else
                            _arguments \
                                '--output[generator output path; omit the value for application.yaml]::output file:_files' \
                                '--disable-bindings[replace disabled bindings for this generator run]:binding:' \
                                '1:global plugin:_conven_global_plugin_names' \
                                '*:plugin argument:'
                        fi
                    elif [[ $words[2] == --* ]]; then
                        _arguments \
                            '--output[generator output path; omit the value for application.yaml]::output file:_files' \
                            '--disable-bindings[replace disabled bindings for this generator run]:binding:' \
                            '*:plugin argument:'
                    else
                        _arguments \
                            $plugin_scope \
                            '--output[generator output path; omit the value for application.yaml]::output file:_files' \
                            '--disable-bindings[replace disabled bindings for this generator run]:binding:' \
                            '1::plugin:_conven_plugin_names' \
                            '*:plugin argument:'
                    fi
                    ;;
                --global)
                    if (( CURRENT == 2 )); then
                        _arguments \
                            '1:global action:(--run)'
                    else
                        words=($words[1] $words[3,-1])
                        (( CURRENT -= 1 ))
                        if (( CURRENT == 2 )); then
                            _arguments \
                                '1:global plugin:_conven_global_plugin_names'
                        else
                            _arguments \
                                '--output[generator output path; omit the value for application.yaml]::output file:_files' \
                                '--disable-bindings[replace disabled bindings for this generator run]:binding:' \
                                '1:global plugin:_conven_global_plugin_names' \
                                '*:plugin argument:'
                        fi
                    fi
                    ;;
                *)
                    if (( CURRENT == 3 )); then
                        _arguments \
                            '--global[force a named user-global plugin run]' \
                            '--install[install a Python plugin]:Python plugin file:_files -g "*.py"' \
                            '--list[list installed plugins]' \
                            '--remove[remove an installed plugin]:plugin:_conven_plugin_names' \
                            '--run[run an installed plugin]' \
                            '--help[show command help]'
                    else
                        _message 'unknown conven plugins action'
                    fi
                    ;;
            esac
            ;;
        doctor)
            _arguments \
                '--env[environment profile]:environment:_conven_environment_names' \
                '--dev[use the dev environment profile]' \
                '--test[use the test environment profile]' \
                '--kubeconfig[kubeconfig path]:file:_files' \
                '--context[kubeconfig context]:context:' \
                '--namespace[Kubernetes namespace]:namespace:' \
                '--help[show command help]'
            ;;
        status)
            _arguments \
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
		return `function __conven_completion_candidates
    set -l raw_tokens (commandline -opc)
    set -l root_args
    set -l index 2
    while test $index -le (count $raw_tokens)
        if test "$raw_tokens[$index]" = -C; and test (math $index + 1) -le (count $raw_tokens)
            set -a root_args -C "$raw_tokens[(math $index + 1)]"
            set index (math $index + 2)
            continue
        end
        break
    end
    command $raw_tokens[1] $root_args __completion candidates $argv 2>/dev/null
end

function __conven_command_tokens
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

function __conven_services_name_position
    set -l tokens (__conven_command_tokens)
    __conven_services_action "$argv[1]"; or return 1
    if contains -- $tokens[-1] --env --kubeconfig --context --namespace
        return 1
    end
    return 0
end

function __conven_services_without_action
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -eq 2; or return 1
    test "$tokens[2]" = services; or return 1
end

function __conven_workspace_without_action
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -eq 2; or return 1
    test "$tokens[2]" = workspace; or return 1
end

function __conven_plugins_without_action
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -eq 2; or return 1
    test "$tokens[2]" = plugins; or return 1
end

function __conven_plugins_action
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -ge 3; or return 1
    test "$tokens[2]" = plugins; or return 1
    test "$tokens[3]" = "$argv[1]"
end

function __conven_plugins_scope_position
    set -l tokens (__conven_command_tokens)
    __conven_plugins_action "$argv[1]"; or return 1
    test (count $tokens) -eq 3
end

function __conven_plugins_global_name_position
    set -l tokens (__conven_command_tokens)
    __conven_plugins_action "$argv[1]"; or return 1
    test (count $tokens) -eq 4; or return 1
    test "$tokens[4]" = --global
end

function __conven_plugins_global_without_action
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -eq 3; or return 1
    test "$tokens[2]" = plugins; or return 1
    test "$tokens[3]" = --global
end

function __conven_plugins_global_run
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -ge 5; or return 1
    test "$tokens[2]" = plugins; or return 1
    test "$tokens[3]" = --global; or return 1
    test "$tokens[4]" = --run
end

function __conven_plugins_global_run_name_position
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -eq 4; or return 1
    test "$tokens[2]" = plugins; or return 1
    test "$tokens[3]" = --global; or return 1
    test "$tokens[4]" = --run
end

function __conven_plugins_run_arguments
    set -l tokens (__conven_command_tokens)
    __conven_plugins_action --run; or return 1
    if test (count $tokens) -ge 4; and test "$tokens[4]" = --global
        test (count $tokens) -ge 5
        return
    end
    return 0
end

function __conven_workspace_action
    set -l tokens (__conven_command_tokens)
    test (count $tokens) -ge 3; or return 1
    test "$tokens[2]" = workspace; or return 1
    test "$tokens[3]" = "$argv[1]"
end

function __conven_workspace_action_without_edit
    set -l tokens (__conven_command_tokens)
    __conven_workspace_action "$argv[1]"; or return 1
    not contains -- --edit $tokens[4..-1]
end

function __conven_workspace_import_without_source
    set -l tokens (__conven_command_tokens)
    __conven_workspace_action --import; or return 1
    for token in $tokens[4..-1]
        string match -q -- '-*' "$token"; or return 1
    end
    return 0
end

complete -c conven -f -n '__conven_global_context' -s C -r -a '(__fish_complete_directories)' -d 'Run as if conven was started in another directory'
complete -c conven -f -n '__conven_without_command' -a init -d 'Initialize a Conven workspace'
complete -c conven -f -n '__conven_without_command' -a services -d 'Manage workspace services'
complete -c conven -f -n '__conven_without_command' -a config -d 'Read or write Conven configuration'
complete -c conven -f -n '__conven_without_command' -a workspace -d 'Edit, validate, migrate, import, or rebuild the workspace manifest'
complete -c conven -f -n '__conven_without_command' -a plugins -d 'Install, list, remove, or run Conven plugins'
complete -c conven -f -n '__conven_without_command' -a status -d 'Show the complete workspace and runtime status'
complete -c conven -f -n '__conven_without_command' -a doctor -d 'Validate workspace configuration'
complete -c conven -f -n '__conven_without_command' -a help -d 'Show conven usage'
complete -c conven -f -n '__conven_without_command' -a version -d 'Show conven version'
complete -c conven -f -n '__conven_using_subcommand help; and __conven_help_without_command' -a 'init services config workspace plugins status doctor help version' -d 'Show detailed command help'
complete -c conven -n '__conven_using_subcommand init services config workspace plugins status doctor' -s h -l help -d 'Show command help'
complete -c conven -n '__conven_using_subcommand init' -l local -d 'Initialize no-cluster local development'
complete -c conven -n '__conven_using_subcommand config' -l global -d 'Use the current user global config'
complete -c conven -n '__conven_using_subcommand config' -l list -d 'List configuration values'
complete -c conven -n '__conven_using_subcommand config' -l unset -d 'Remove one configuration value'
complete -c conven -n '__conven_using_subcommand workspace; and __conven_workspace_without_action' -l edit -d 'Edit a validated temporary manifest copy'
complete -c conven -n '__conven_using_subcommand workspace; and __conven_workspace_without_action' -l validate -d 'Validate the current workspace manifest'
complete -c conven -n '__conven_using_subcommand workspace; and __conven_workspace_without_action' -l migrate -d 'Atomically migrate a stopped v1/v2 manifest to v3'
complete -c conven -n '__conven_using_subcommand workspace; and __conven_workspace_without_action' -l import -d 'Import a local YAML file as the entire manifest'
complete -c conven -n '__conven_using_subcommand workspace; and __conven_workspace_without_action' -l reset -d 'Destructively reset the manifest to scanned facts'
complete -c conven -n '__conven_workspace_action_without_edit --import' -l edit -d 'Edit the private import draft before publication'
complete -c conven -n '__conven_workspace_import_without_source' -F
complete -c conven -n '__conven_using_subcommand plugins; and __conven_plugins_without_action' -l install -r -a '(__fish_complete_suffix .py)' -d 'Install a Python plugin'
complete -c conven -n '__conven_using_subcommand plugins; and __conven_plugins_without_action' -l list -d 'List installed plugins'
complete -c conven -n '__conven_using_subcommand plugins; and __conven_plugins_without_action' -l remove -r -d 'Remove an installed plugin'
complete -c conven -n '__conven_using_subcommand plugins; and __conven_plugins_without_action' -l run -d 'Run an installed plugin'
complete -c conven -n '__conven_using_subcommand plugins; and __conven_plugins_without_action' -l global -d 'Force a named user-global plugin run'
complete -c conven -n '__conven_plugins_global_without_action' -l run -d 'Run a named user-global plugin'
complete -c conven -n '__conven_plugins_scope_position --install' -l global -d 'Install in the user-global plugin directory'
complete -c conven -n '__conven_plugins_scope_position --list' -l global -d 'List only user-global plugins'
complete -c conven -n '__conven_plugins_scope_position --remove' -l global -d 'Remove from the user-global plugin directory'
complete -c conven -n '__conven_plugins_scope_position --run' -l global -d 'Force the user-global plugin scope'
complete -c conven -n '__conven_plugins_run_arguments' -l output -F -d 'Generator output path; omit its value for application.yaml'
complete -c conven -n '__conven_plugins_run_arguments' -l disable-bindings -r -d 'Replace disabled bindings for this generator run'
complete -c conven -n '__conven_plugins_global_run' -l output -F -d 'Generator output path; omit its value for application.yaml'
complete -c conven -n '__conven_plugins_global_run' -l disable-bindings -r -d 'Replace disabled bindings for this generator run'
complete -c conven -f -n '__conven_plugins_scope_position --remove' -a '(__conven_completion_candidates plugins)' -d 'Installed plugin'
complete -c conven -f -n '__conven_plugins_global_name_position --remove' -a '(__conven_completion_candidates plugins global)' -d 'User-global plugin'
complete -c conven -f -n '__conven_plugins_scope_position --run' -a '(__conven_completion_candidates plugins)' -d 'Installed plugin'
complete -c conven -f -n '__conven_plugins_global_name_position --run' -a '(__conven_completion_candidates plugins global)' -d 'User-global plugin'
complete -c conven -f -n '__conven_plugins_global_run_name_position' -a '(__conven_completion_candidates plugins global)' -d 'User-global plugin'
complete -c conven -n '__conven_using_subcommand doctor' -l env -r -a '(__conven_completion_candidates environments)' -d 'Environment profile'
complete -c conven -n '__conven_using_subcommand doctor' -l dev -d 'Use the dev environment profile'
complete -c conven -n '__conven_using_subcommand doctor' -l test -d 'Use the test environment profile'
complete -c conven -n '__conven_using_subcommand doctor' -l kubeconfig -r -F -d 'Kubeconfig path'
complete -c conven -n '__conven_using_subcommand doctor' -l context -r -d 'Kubeconfig context'
complete -c conven -n '__conven_using_subcommand doctor' -l namespace -r -d 'Kubernetes namespace'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l list -d 'List services declared by the workspace'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l registry -d 'Update services from child repositories'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l listen -d 'Change listener scope for selected services'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l status -d 'Show current local service state'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l logs -d 'Show or stream current session logs'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l dashboard -d 'Open the interactive log dashboard'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l start -d 'Select and start local services'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l restart -d 'Restart changed local services'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l stop -d 'Stop selected local services'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l stop-all -d 'Stop all services and release the workspace connection'
complete -c conven -n '__conven_using_subcommand services; and __conven_services_without_action' -l cleanup -d 'Remove saved build artifacts and service logs'
complete -c conven -n '__conven_services_action --registry' -l prune -d 'Remove missing direct-child repository services'
complete -c conven -n '__conven_services_action --listen' -l on -d 'Listen on all interfaces'
complete -c conven -n '__conven_services_action --listen' -l off -d 'Restore loopback-only listening'
complete -c conven -n '__conven_services_action --logs' -l tail -d 'Stream aggregated logs as plain text'
complete -c conven -n '__conven_services_action --logs' -l dashboard -d 'Open the interactive log dashboard'
complete -c conven -n '__conven_services_action --start' -l env -r -a '(__conven_completion_candidates environments)' -d 'Environment profile'
complete -c conven -n '__conven_services_action --start' -l dev -d 'Use the dev environment profile'
complete -c conven -n '__conven_services_action --start' -l test -d 'Use the test environment profile'
complete -c conven -n '__conven_services_action --start' -l kubeconfig -r -F -d 'Kubeconfig path'
complete -c conven -n '__conven_services_action --start' -l context -r -d 'Kubeconfig context'
complete -c conven -n '__conven_services_action --start' -l namespace -r -d 'Kubernetes namespace'
complete -c conven -n '__conven_services_action --start' -l tail -d 'Stream aggregated logs as plain text'
complete -c conven -n '__conven_services_action --start' -l dry-run -d 'Show the startup plan'
complete -c conven -n '__conven_services_action --start' -l with-dependencies -d 'Also start transitive local service dependencies'
complete -c conven -n '__conven_services_action --start' -l skip-build -d 'Skip build when artifacts are reusable'
complete -c conven -n '__conven_services_action --start' -l skip-verify -d 'Skip health, listener, and registry verification'
complete -c conven -n '__conven_services_action --restart' -l tail -d 'Stream aggregated logs as plain text'
complete -c conven -n '__conven_services_action --restart' -l dashboard -d 'Open the interactive log dashboard'
complete -c conven -n '__conven_services_action --restart' -l skip-build -d 'Skip build when artifacts are reusable'
complete -c conven -n '__conven_services_action --restart' -l skip-verify -d 'Skip health, listener, and registry verification'
complete -c conven -n '__conven_services_action --stop' -l all -d 'Stop every service and release the workspace connection'
complete -c conven -n '__conven_services_action --stop' -l force -d 'Bypass identity checks and recover saved process groups'
complete -c conven -n '__conven_services_action --stop-all' -l force -d 'Bypass identity checks and recover saved process groups'
complete -c conven -f -n '__conven_services_name_position --listen' -a '(__conven_completion_candidates services)' -d 'Workspace service'
complete -c conven -f -n '__conven_services_name_position --logs' -a '(__conven_completion_candidates services)' -d 'Workspace service'
complete -c conven -f -n '__conven_services_name_position --dashboard' -a '(__conven_completion_candidates services)' -d 'Workspace service'
complete -c conven -f -n '__conven_services_name_position --start' -a '(__conven_completion_candidates services)' -d 'Workspace service'
complete -c conven -f -n '__conven_services_name_position --restart' -a '(__conven_completion_candidates services)' -d 'Workspace service'
complete -c conven -f -n '__conven_services_name_position --stop' -a '(__conven_completion_candidates services)' -d 'Workspace service'
`, nil
	default:
		return "", fmt.Errorf("unsupported completion shell %q", shell)
	}
}
