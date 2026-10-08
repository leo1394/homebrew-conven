class Conven < Formula
  desc "Run a focused set of local microservices with remote dependencies"
  homepage "https://github.com/leo1394/homebrew-conven"
  url "https://github.com/leo1394/homebrew-conven/archive/refs/tags/v1.1.2.tar.gz"
  sha256 "978cfa8e2e6a325c7e6474fe886f975d2ebc4d83a20d852b5bdd8d5a943b6169"
  license "MIT"
  head "https://github.com/leo1394/homebrew-conven.git", branch: "master"

  bottle do
    root_url "https://github.com/leo1394/homebrew-conven/releases/download/conven-1.1.2"
    sha256 cellar: :any_skip_relocation, arm64_tahoe:   "8db3f11317035df8447b73c9499e583febcf132fc5d6fc6bc6be67579cc82d54"
    sha256 cellar: :any_skip_relocation, arm64_sequoia: "217bb47c9ba071047b6cf6c332686125f9d3deca32779c184c65f684dfee2a5d"
    sha256 cellar: :any,                 x86_64_linux:  "a1bcbd3ab9ba508bf3cff906d3115ac49f4fea273308011736681003f84ae851"
  end

  depends_on "go" => :build

  def install
    system "go", "build", *std_go_args(ldflags: "-s -w"), "./cmd/conven"
    generate_completions_from_executable(bin/"conven", "__completion")
    man1.install "docs/conven.1" if build.head? || version >= "0.2.4"
  end

  test do
    ENV["HOME"] = testpath.to_s
    ENV["LC_ALL"] = "en_US.UTF-8"
    expected_version = build.head? ? "conven version 1.1.2 (2026-10-09)" : "conven version #{version} ("
    assert_match expected_version, shell_output("#{bin}/conven --version")
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
