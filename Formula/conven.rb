class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v0.3.3.tar.gz"
  sha256 "a1a212a15aaaa7ff263c2c74e888ca84753cb9bba09a67aaaa5b2ef25ca52d24"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git", branch: "master"

  bottle do
    root_url "https://github.com/leo1394/homebrew-conven/releases/download/conven-0.3.3"
    sha256 cellar: :any_skip_relocation, arm64_tahoe:   "f481fa88ae0bfcb14ba77dd81da22abd6cf6b1a849fe398f7ed052269d87fb28"
    sha256 cellar: :any_skip_relocation, arm64_sequoia: "8efc33be8f745d3626f6962f3c82e33b0d889402abd889f06083b9aba15609da"
    sha256 cellar: :any,                 x86_64_linux:  "e944c2f0a1a28a947a3564c44813dff66496c5b0de868aa4799edc260d6b1e6e"
  end

  depends_on "go" => :build
  depends_on "python" => :test

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w"), "./cmd/conven"
    generate_completions_from_executable(bin/"conven", "__completion")
    man1.install "docs/conven.1" if build.head? || version >= "0.2.4"
  end

  test do
    ENV["HOME"] = testpath.to_s
    ENV["LC_ALL"] = "en_US.UTF-8"
    new_cli = build.head? || version >= "0.2.9"
    scoped_plugins = build.head? || version >= "0.2.11"
    plugin_selector = build.head? || version >= "0.2.12"
    unified_workspace_manifest = build.head? || version >= "0.4.0"
    workspace_catalog = !unified_workspace_manifest && version >= "0.2.13"
    workspace_status = workspace_catalog || unified_workspace_manifest
    generalized_bindings = build.head? || version >= "0.3.0"
    if build.head?
      expected_version = <<~EOS
        conven version 1.0.1 (2026-09-02)
        https://github.com/leo1394/homebrew-conven
      EOS
      expected_version = Regexp.new("#{Regexp.escape(expected_version)}\\z")
    elsif new_cli
      expected_version = Regexp.new(
        "conven version #{Regexp.escape(version.to_s)} " \
        "\\(\\d{4}-\\d{2}-\\d{2}\\)\\n" \
        "https://github\\.com/leo1394/homebrew-conven\\n\\z",
      )
    else
      expected_version = /\Aconven #{Regexp.escape(version.to_s)}\n\z/
    end
    assert_match expected_version, shell_output("#{bin}/conven --version")
    assert_predicate bin/"conven", :executable?
    assert_path_exists man1/"conven.1" if build.head? || version >= "0.2.4"

    workspace = testpath/"workspace"
    workspace.mkpath
    Dir.chdir(workspace) do
      workspace_state = workspace/".conven"
      manifest = workspace_state/"conven.yaml"
      system bin/"conven", "init"
      assert_path_exists manifest
      if unified_workspace_manifest
        refute_path_exists workspace_state/"catalog.yaml"
        system bin/"conven", "workspace", "--validate"
        status = shell_output("#{bin}/conven status")
        assert_includes status, "Workspace"
        assert_includes status, "Disabled bindings"
        assert_includes status, "No Conven session found."
      elsif workspace_catalog
        system bin/"conven", "catalog", "--validate"
        status = shell_output("#{bin}/conven status")
        assert_includes status, "Workspace"
        expected_disabled_bindings = generalized_bindings ? "Disabled bindings" : "Disabled RPC bindings"
        assert_includes status, expected_disabled_bindings
        assert_includes status, "No Conven session found."
      end
      if scoped_plugins
        workspace_assets = %w[
          CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC.md
          README.md
        ]
        workspace_assets.unshift(".conven/catalog.yaml") if workspace_catalog
        workspace_assets.each do |filename|
          assert_path_exists workspace/filename
        end
        specification = workspace/"CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC.md"
        assert_includes specification.read, "language: en"
        assert_includes specification.read, "https://github.com/leo1394/homebrew-conven"
        refute_path_exists workspace/"CONVEN-WORKSPACE-POLICY-GENERATOR-AI-SPEC-EN.md"
        workspace_asset_contents = workspace_assets.to_h { |filename| [filename, (workspace/filename).read] }
      end
      plugin_directory = scoped_plugins ? workspace_state/"plugins" : testpath/".conven/plugins"
      unless scoped_plugins
        assert_predicate plugin_directory, :directory?
        assert_empty plugin_directory.children
      end
      plugin_source = testpath/"formula-plugin.py"
      plugin_source.write("#!/usr/bin/env python3\nprint('formula plugin')\n")
      plugin_source.chmod(0700)
      system bin/"conven", "plugins", "--install", plugin_source
      plugin = plugin_directory/"formula-plugin.py"
      assert_predicate plugin_directory, :directory?
      assert_path_exists plugin
      assert_predicate plugin, :executable?
      plugin_list = shell_output("#{bin}/conven plugins --list")
      if scoped_plugins
        assert_includes plugin_list, "Workspace plugins"
        assert_includes plugin_list, "Global plugins"
        expected_plugin_line = plugin_selector ? "  - formula-plugin\n" : "  formula-plugin\n"
        assert_includes plugin_list.lines, expected_plugin_line
        local_run = shell_output("#{bin}/conven plugins --run 2>&1")
        if plugin_selector
          assert_includes local_run, "Warning: Plugin name omitted; selected the only workspace plugin."
          assert_includes local_run, "  - Plugin: formula-plugin"
        else
          assert_includes local_run, "running workspace plugin formula-plugin"
        end
        assert_includes local_run, "formula plugin"
        global_plugin_source = testpath/"global-formula-plugin.py"
        global_plugin_source.write("#!/usr/bin/env python3\nprint('global formula plugin')\n")
        global_plugin_source.chmod(0700)
        system bin/"conven", "plugins", "--install", "--global", global_plugin_source
        assert_path_exists testpath/".conven/plugins/global-formula-plugin.py"
        assert_includes shell_output("#{bin}/conven plugins --list --global"), "global-formula-plugin"
        global_run_command = if plugin_selector
          "#{bin}/conven plugins --global --run global-formula-plugin 2>&1"
        else
          "#{bin}/conven plugins --run global-formula-plugin 2>&1"
        end
        global_run = shell_output(global_run_command)
        if plugin_selector
          assert_includes global_run, "Warning: Running a global plugin."
          assert_includes global_run, "  - Plugin: global-formula-plugin"
        else
          assert_includes global_run, "running global plugin global-formula-plugin"
        end
        assert_includes global_run, "global formula plugin"
        if plugin_selector
          missing_global_name = shell_output("#{bin}/conven plugins --global --run 2>&1", 1)
          assert_includes missing_global_name, "plugins --global --run requires a plugin name"
        end
      else
        assert_includes plugin_list.lines, "formula-plugin\n"
      end
      manifest.write("\n# preserve this line\n", mode: "a")
      expected_manifest = manifest.read
      system bin/"conven", "init"
      assert_equal expected_manifest, manifest.read
      if scoped_plugins
        workspace_asset_contents.each do |filename, contents|
          assert_equal contents, (workspace/filename).read
        end
        catalog_files = workspace_catalog ? %w[.conven/catalog.yaml] : []
        catalog_contents = catalog_files.to_h do |filename|
          [filename, (workspace/filename).read]
        end
        system bin/"conven", "services", "--registry"
        catalog_contents.each do |filename, contents|
          assert_equal contents, (workspace/filename).read
        end
      end

      imported_policy = workspace/(scoped_plugins ? "application.yaml" : "imported-policy.yaml")
      imported_manifest = expected_manifest.sub(/^  name: .+$/, "  name: formula-import")
      refute_equal expected_manifest, imported_manifest
      imported_policy.write(imported_manifest)
      manifest_command = unified_workspace_manifest ? "workspace" : "policy"
      system bin/"conven", manifest_command, "--import", imported_policy
      assert_equal imported_manifest, manifest.read
      assert_predicate workspace_state/"backups", :directory?
      import_backups = (workspace_state/"backups").children
      assert_equal 1, import_backups.length
      assert_equal expected_manifest, import_backups.first.read
    end

    system bin/"conven", "-C", workspace, "services", "--list" if new_cli

    assert_path_exists bash_completion/"conven"
    assert_path_exists zsh_completion/"_conven"
    assert_path_exists fish_completion/"conven.fish"
    top_level_commands = %w[init services config policy plugins doctor help version]
    if unified_workspace_manifest
      top_level_commands.delete("policy")
      top_level_commands.insert(3, "workspace")
    end
    top_level_commands.insert(3, "catalog") if workspace_catalog
    top_level_commands.insert(6, "status") if workspace_status
    service_actions = %w[list registry status logs start restart stop stop-all]
    service_actions.insert(4, "dashboard") if build.head? || version >= "0.2.5"
    service_actions << "cleanup" if build.head? || version >= "0.2.7"
    service_actions << "listen" if build.head? || version >= "0.3.3"
    policy_actions = unified_workspace_manifest ? [] : %w[edit import reset]
    workspace_actions = unified_workspace_manifest ? %w[edit validate migrate import reset] : []
    catalog_actions = workspace_catalog ? %w[edit validate] : []
    plugin_actions = %w[install list run]
    plugin_actions.insert(2, "remove") if build.head? || version >= "0.2.2"
    removed_top_level_commands = %w[discover list logs start restart stop]
    removed_top_level_commands.push("catalog", "policy") if unified_workspace_manifest
    removed_top_level_commands.insert(2, "status") unless workspace_status
    %w[bash zsh fish].each do |shell|
      completion = shell_output("#{bin}/conven __completion #{shell}")
      case shell
      when "bash"
        top_level = completion[/compgen -W "([^"]+)"/, 1]
        refute_nil top_level
        candidates = top_level.split
        assert_includes candidates, "-C" if new_cli
        top_level_commands.each do |command|
          assert_includes candidates, command
        end
        removed_top_level_commands.each do |command|
          refute_includes candidates, command
        end
      when "zsh"
        top_level_commands.each do |command|
          assert_includes completion, "        '#{command}:"
        end
        removed_top_level_commands.each do |command|
          refute_includes completion, "        '#{command}:"
        end
      when "fish"
        fish_root_condition = new_cli ? "__conven_without_command" : "__fish_use_subcommand"
        top_level_commands.each do |command|
          assert_includes completion, "complete -c conven -f -n '#{fish_root_condition}' -a #{command} "
        end
        removed_top_level_commands.each do |command|
          refute_includes completion, "complete -c conven -f -n '#{fish_root_condition}' -a #{command} "
        end
      end
      service_actions.each do |action|
        if shell == "fish"
          assert_includes completion, "-l #{action}"
        else
          assert_includes completion, "--#{action}"
        end
      end
      policy_actions.each do |action|
        if shell == "fish"
          assert_includes completion, "-l #{action}"
        else
          assert_includes completion, "--#{action}"
        end
      end
      workspace_actions.each do |action|
        if shell == "fish"
          assert_includes completion, "-l #{action}"
        else
          assert_includes completion, "--#{action}"
        end
      end
      catalog_actions.each do |action|
        if shell == "fish"
          assert_includes completion, "-l #{action}"
        else
          assert_includes completion, "--#{action}"
        end
      end
      plugin_actions.each do |action|
        if shell == "fish"
          assert_includes completion, "-l #{action}"
        else
          assert_includes completion, "--#{action}"
        end
      end
      if shell == "fish"
        assert_includes completion, "-s C" if new_cli
        assert_includes completion, "-l dev"
        assert_includes completion, "-l test"
        assert_includes completion, "-l tail"
        assert_includes completion, "-l prune"
        refute_includes completion, "-l follow"
        refute_includes completion, "-l workspace"
        refute_includes completion, "-l config"
        assert_includes completion, "-l global" if scoped_plugins
      else
        assert_includes completion, "-C" if new_cli
        assert_includes completion, "--dev"
        assert_includes completion, "--test"
        assert_includes completion, "--tail"
        assert_includes completion, "--prune"
        refute_includes completion, "--follow"
        refute_includes completion, "--workspace"
        refute_includes completion, "--config"
        assert_includes completion, "--global" if scoped_plugins
      end
      refute_includes completion, "convening"
    end
  end
end
