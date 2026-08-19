module Library
  # What each title on a disc is for, when nobody has said.
  #
  # Filing executes decisions; it does not make them. But a person confirming
  # "this is Roman Holiday, 1953" has told us everything that matters and
  # should not also have to tick which of seven titles is the film. So an
  # unset role on a *film* disc is filled in from the disc's shape, using the
  # rule v1 arrived at against real discs.
  #
  # A television disc gets no such help and is not meant to. Its titles need
  # episode numbers, no shape carries those, and guessing them wrongly files a
  # season one episode out of step -- which looks plausible and is the worst
  # way to be wrong. Unset roles there stay unset and are reported.
  #
  # A role a person did set is never second-guessed, on either kind of disc.
  module Roles
    # How much longer the feature must run than the disc's median title. A film
    # disc is lopsided; a disc of episodes is not. Measured against the median
    # rather than the second-longest because a DVD routinely carries its
    # feature twice -- widescreen and fullscreen, or the branches of a seamless
    # disc -- and comparing the top two made the most obviously film-shaped
    # disc in the collection look like neither.
    FEATURE_RATIO = 2.0

    # Long enough to be a film at all. Keeps a feature-length extra, or a
    # single long episode, from being promoted.
    MIN_FEATURE_SECONDS = 45 * 60

    # Returns { title_index => role }, covering every title on the disc.
    def self.for(disc)
      set = disc.disc_titles.map { |t| [t.index, t.role] }.to_h
      return set unless disc.confirmed_kind == "movie"
      return set if set.value?("feature") # a person already picked one

      feature = feature_index(disc)
      disc.disc_titles.each do |t|
        next unless set[t.index] == "unknown"
        set[t.index] = t.index == feature ? "feature" : "extra"
      end
      set
    end

    # The title that is the film, or nil if the disc is not shaped like one.
    def self.feature_index(disc)
      durations = disc.disc_titles.map { |t| t.duration_seconds.to_i }
      return nil if durations.empty?

      longest = disc.disc_titles.max_by { |t| t.duration_seconds.to_i }
      return nil if longest.duration_seconds.to_i < MIN_FEATURE_SECONDS

      # A disc holding only the feature has no median to be lopsided against,
      # and is the easiest case there is.
      return longest.index if durations.size == 1

      mid = median(durations)
      return longest.index if mid.zero?
      longest.duration_seconds >= mid * FEATURE_RATIO ? longest.index : nil
    end

    def self.median(values)
      sorted = values.sort
      mid = sorted.size / 2
      sorted.size.odd? ? sorted[mid] : ((sorted[mid - 1] + sorted[mid]) / 2.0).round
    end
    private_class_method :median
  end
end
