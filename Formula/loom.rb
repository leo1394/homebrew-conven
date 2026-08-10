class Loom < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-loom"
  url "https://github.com/leo1394/homebrew-loom/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "16f53e4a519539f2b889fbd24fd27d5e8b5d61600ffd9e805ce7c84dcc7497cd"
  license "MIT"
  head "https://github.com/leo1394/homebrew-loom.git"

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w"), "./cmd/loom"
    generate_completions_from_executable(bin/"loom", "__completion")
  end

  test do
    assert_equal "loom 0.1.0\n", shell_output("#{bin}/loom --version")
    assert_predicate bin/"loom", :executable?

    manifest = testpath/".loom/loom.yaml"
    system bin/"loom", "init"
    assert_path_exists manifest
    manifest.write("#{manifest.read}\n# preserve this line\n")
    expected_manifest = manifest.read
    system bin/"loom", "init"
    assert_equal expected_manifest, manifest.read

    imported_policy = testpath/"imported-policy.yaml"
    imported_manifest = expected_manifest.sub(/^  name: .+$/, "  name: formula-import")
    refute_equal expected_manifest, imported_manifest
    imported_policy.write(imported_manifest)
    system bin/"loom", "policy", "--import", imported_policy
    assert_equal imported_manifest, manifest.read
    assert_predicate testpath/".loom/backups", :directory?
    import_backups = (testpath/".loom/backups").children
    assert_equal 1, import_backups.length
    assert_equal expected_manifest, import_backups.first.read

    assert_path_exists bash_completion/"loom"
    assert_path_exists zsh_completion/"_loom"
    assert_path_exists fish_completion/"loom.fish"
    top_level_commands = %w[init services config policy doctor help version]
    service_actions = %w[list registry status logs start restart stop stop-all]
    policy_actions = %w[edit import reset]
    removed_top_level_commands = %w[discover list status logs start restart stop]
    %w[bash zsh fish].each do |shell|
      completion = shell_output("#{bin}/loom __completion #{shell}")
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
          assert_includes completion, "complete -c loom -f -n '__fish_use_subcommand' -a #{command} "
        end
        removed_top_level_commands.each do |command|
          refute_includes completion, "complete -c loom -f -n '__fish_use_subcommand' -a #{command} "
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
      refute_includes completion, "looming"
    end
  end
end
