class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v0.2.10.tar.gz"
  sha256 "52140ab275496567c635891e57952c3434d3418eaf46ab8dff40b574e654cb8b"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git", branch: "master"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w"), "./cmd/conven"
    generate_completions_from_executable(bin/"conven", "__completion")
    man1.install "docs/conven.1" if build.head? || version >= "0.2.4"
  end

  test do
    ENV["HOME"] = testpath.to_s
    new_cli = build.head? || version >= "0.2.9"
    if build.head?
      expected_version = <<~EOS
        conven version 0.2.10 (2026-08-12)
        https://github.com/leo1394/homebrew-conven
      EOS
      expected_version = Regexp.new("\\A#{Regexp.escape(expected_version)}\\z")
    elsif new_cli
      expected_version = Regexp.new(
        "\\Aconven version #{Regexp.escape(version.to_s)} " \
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
      manifest.write("\n# preserve this line\n", mode: "a")
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

    system bin/"conven", "-C", workspace, "services", "--list" if new_cli

    assert_path_exists bash_completion/"conven"
    assert_path_exists zsh_completion/"_conven"
    assert_path_exists fish_completion/"conven.fish"
    top_level_commands = %w[init services config policy plugins doctor help version]
    service_actions = %w[list registry status logs start restart stop stop-all]
    service_actions.insert(4, "dashboard") if build.head? || version >= "0.2.5"
    service_actions << "cleanup" if build.head? || version >= "0.2.7"
    policy_actions = %w[edit import reset]
    plugin_actions = %w[install list run]
    plugin_actions.insert(2, "remove") if build.head? || version >= "0.2.2"
    removed_top_level_commands = %w[discover list status logs start restart stop]
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
      else
        assert_includes completion, "-C" if new_cli
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
