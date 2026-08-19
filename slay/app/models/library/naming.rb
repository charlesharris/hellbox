module Library
  # The names Jellyfin expects, derived from what a person confirmed.
  #
  # Pure: nothing here touches the filesystem or the database, which is what
  # makes the layout checkable without a disc, a mount, or a container. Every
  # decision about what a file is called lives here and nowhere else, so a
  # rename is one edit rather than a hunt.
  #
  # The layout is Jellyfin's documented one and is not a matter of taste:
  #
  #   Movies/Roman Holiday (1953)/Roman Holiday (1953).mkv
  #                              /Roman Holiday (1953).nfo
  #                              /extras/Roman Holiday (1953) - Deleted Scenes.mkv
  #   TV/Still Game/tvshow.nfo
  #                /Season 03/Still Game - S03E05 - Doacters.mkv
  #                          /Still Game - S03E05 - Doacters.nfo
  module Naming
    # Characters that cannot appear in a filename on the filesystems this
    # library might live on. Only `/` and NUL are illegal on Linux, but a media
    # library is routinely read over SMB by a television, and a colon that
    # works here becomes an unreadable file there. Widening the rule to
    # Windows' set costs nothing and avoids a failure that only shows up on the
    # device furthest from the keyboard.
    ILLEGAL = %r{[/\\:*?"<>|\x00-\x1f]}

    # Trailing dots and spaces are stripped for the same reason: legal here,
    # silently mangled over SMB.
    def self.clean(str)
      str.to_s.gsub(ILLEGAL, " ").gsub(/\s+/, " ").strip.sub(/[. ]+\z/, "")
    end

    # A film's folder and its base filename are the same string, which is what
    # lets Jellyfin pair a file with its sidecar without being told.
    def self.movie_base(title, year)
      name = clean(title)
      name = "Unsorted" if name.blank?
      year.to_i.positive? ? "#{name} (#{year.to_i})" : name
    end

    def self.movie_dir(title, year)
      File.join(Paths.movies, movie_base(title, year))
    end

    def self.movie_file(title, year)
      base = movie_base(title, year)
      File.join(Paths.movies, base, "#{base}.mkv")
    end

    def self.series_dir(title)
      name = clean(title)
      name = "Unsorted" if name.blank?
      File.join(Paths.tv, name)
    end

    def self.season_dir(title, season)
      File.join(series_dir(title), format("Season %02d", season.to_i))
    end

    # "Still Game - S03E05 - Doacters", or the same without the episode title
    # when nobody has typed one. The SxxExx block is what Jellyfin actually
    # parses; the name after it is for a person reading a directory listing.
    def self.episode_base(series, season, episode, episode_title = nil)
      base = format("%s - S%02dE%02d", clean(series), season.to_i, episode.to_i)
      name = clean(episode_title)
      name.present? ? "#{base} - #{name}" : base
    end

    def self.episode_file(series, season, episode, episode_title = nil)
      File.join(season_dir(series, season),
                "#{episode_base(series, season, episode, episode_title)}.mkv")
    end

    # Extras live beside the thing they are extra to, which is how Jellyfin
    # tells a featurette from a second cut of the feature.
    #
    # Jellyfin also supports typed suffixes -- `-featurette`, `-deleted`,
    # `-behindthescenes` -- but typing an extra needs a name to type it from,
    # and the only source of those names is menu OCR, which is not built. So
    # this is v1's untyped fallback, and it is deliberate rather than
    # forgotten: an extra correctly filed as an extra is the property that
    # matters, and the suffix is a refinement on top of it.
    def self.extra_file(parent_dir, base, name, index)
      leaf = clean(name)
      leaf = format("t%02d", index.to_i) if leaf.blank?
      File.join(parent_dir, "extras", "#{clean(base)} - #{leaf}.mkv")
    end

    # An NFO sits beside its film or episode under the same base name. A
    # series' own tvshow.nfo is the one exception and is named literally.
    def self.sidecar(media_path)
      media_path.sub(/\.mkv\z/i, ".nfo")
    end
  end
end
