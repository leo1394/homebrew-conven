class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v1.0.5.tar.gz"
  sha256 "cd2624a2cbe4087a9d460800170fdfeec2188d86605d2ceaa23da684b65f5638"
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
    ENV["LC_ALL"] = "en_US.UTF-8"
    assert_match "conven version 1.0.5 (2026-09-11)", shell_output("#{bin}/conven --version")
    assert_predicate bin/"conven", :executable?
    assert_path_exists man1/"conven.1"
    assert_path_exists bash_completion/"conven"
    assert_path_exists zsh_completion/"_conven"
    assert_path_exists fish_completion/"conven.fish"

    workspace = testpath/"workspace"
    workspace.mkpath
    system bin/"conven", "-C", workspace, "init"
    assert_path_exists workspace/".conven/conven.yaml"
    system bin/"conven", "-C", workspace, "workspace", "--validate"
  end
end
