class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v0.2.1.tar.gz"
  sha256 "52f39697d6672848da7f580f8a74baf5a270522f6e2beeac7ffba30b4a4047c9"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w"), "./cmd/conven"
    generate_completions_from_executable(bin/"conven", "__completion")
  end

  test do
    ENV["HOME"] = testpath.to_s
    assert_equal "conven 0.2.1\n", shell_output("#{bin}/conven --version")
    assert_predicate bin/"conven", :executable?

    workspace = testpath/"workspace"
    workspace.mkpath
    Dir.chdir(workspace) do
      workspace_state = workspace/(build.head? ? ".conven" : ".loom")
      manifest = workspace_state/(build.head? ? "conven.yaml" : "loom.yaml")
      system bin/"conven", "init"
      assert_path_exists manifest
      if build.head?
        plugin_directory = testpath/".conven/plugins"
        assert_predicate plugin_directory, :directory?
        assert_empty plugin_directory.children
        plugin_source = testpath/"formula-plugin.py"
        plugin_source.write("#!/usr/bin/env python3\nprint('formula plugin')\n")
        plugin_source.chmod(0700)
        system bin/"conven", "plugins", "--install", plugin_source
        plugin = plugin_directory/"formula-plugin.py"
        assert_path_exists plugin
        assert_predicate plugin, :executable?
        assert_includes shell_output("#{bin}/conven plugins --list").lines, "formula-plugin\n"
      end
      manifest.write("#{manifest.read}\n# preserve this line\n")
      expected_manifest = manifest.read
      system bin/"conven", "init"
      assert_equal expected_manifest, manifest.read

      imported_policy = workspace/"imported-policy.yaml"
      imported_manifest = expected_manifest.sub(/^  name: .+$/, "  name: formula-import")
      refute_equal expected_manifest, imported_manifest
      imported_policy.write(imported_manifest)
      system bin/"conven", "policy", "--import", imported_policy
      assert_equal imported_manifest, manifest.read
      assert_predicate workspace_state/"backups", :directory?
      import_backups = (workspace_state/"backups").children
      assert_equal 1, import_backups.length
      assert_equal expected_manifest, import_backups.first.read
    end

    assert_path_exists bash_completion/"conven"
    assert_path_exists zsh_completion/"_conven"
    assert_path_exists fish_completion/"conven.fish"
    top_level_commands = %w[init services config policy doctor help version]
    top_level_commands.insert(4, "plugins") if build.head?
    service_actions = %w[list registry status logs start restart stop stop-all]
    policy_actions = %w[edit import reset]
    plugin_actions = build.head? ? %w[install list run] : []
    removed_top_level_commands = %w[discover list status logs start restart stop]
    %w[bash zsh fish].each do |shell|
      completion = shell_output("#{bin}/conven __completion #{shell}")
      case shell
      when "bash"
        top_level = completion[/compgen -W "([^"]+)"/, 1]
        refute_nil top_level
        candidates = top_level.split
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
        top_level_commands.each do |command|
          assert_includes completion, "complete -c conven -f -n '__fish_use_subcommand' -a #{command} "
        end
        removed_top_level_commands.each do |command|
          refute_includes completion, "complete -c conven -f -n '__fish_use_subcommand' -a #{command} "
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
      plugin_actions.each do |action|
        if shell == "fish"
          assert_includes completion, "-l #{action}"
        else
          assert_includes completion, "--#{action}"
        end
      end
      if shell == "fish"
        assert_includes completion, "-l dev"
        assert_includes completion, "-l test"
        assert_includes completion, "-l tail"
        assert_includes completion, "-l prune"
        refute_includes completion, "-l follow"
        refute_includes completion, "-l workspace"
        refute_includes completion, "-l config"
      else
        assert_includes completion, "--dev"
        assert_includes completion, "--test"
        assert_includes completion, "--tail"
        assert_includes completion, "--prune"
        refute_includes completion, "--follow"
        refute_includes completion, "--workspace"
        refute_includes completion, "--config"
      end
      refute_includes completion, "convening"
    end
  end
end
