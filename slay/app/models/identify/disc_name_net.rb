module Identify
  # Reads the name the disc gives itself.
  #
  # The strongest evidence available before anything is ripped. A DVD keeps it
  # in the Text Data Manager inside VIDEO_TS.IFO; a Blu-ray in
  # BDMV/META/DL/bdmt_eng.xml. Neither is encrypted, so both are readable with
  # no key at all — even on a disc nothing can decrypt.
  #
  # The Karate Kid, whose volume label is DVD_VIDEO, holds "The Karate Kid
  # (Special Edition)" there. Firefly disc 1, labelled FIREFLYUS_D1, holds
  # "FIREFLY: DISC 1".
  class DiscNameNet
    EDITIONS = [
      "special edition", "collector's edition", "collectors edition",
      "deluxe edition", "extended edition", "director's cut", "directors cut",
      "unrated", "uncut", "remastered", "restored", "anniversary edition",
      "limited edition", "ultimate edition", "widescreen", "fullscreen",
      "full screen", "wide screen", "theatrical cut", "theatrical version",
      "2-disc set", "two-disc set", "bonus disc"
    ].freeze

    TRAILING_PAREN = /\A(.*?)\s*\(([^()]{1,40})\)\s*\z/
    YEAR_ONLY      = /\A(?:19|20)\d{2}\z/
    DISC_IN_NAME   = /\A(.*?)[\s:,-]+dis[ck]\s*(\d{1,2})\s*\z/i
    SEASON_IN_NAME = /\A(.*?)[\s:,-]+(?:season|series)\s*(\d{1,2})\s*\z/i

    def name = "discname"

    def identify(disc)
      raw = disc.disc_name.to_s.strip
      return [] if raw.empty?
      return [] if LabelNet::JUNK.include?(raw.downcase.tr(" ", "_"))

      source = disc.disc_type == "bluray" ? "bdmt_eng.xml" : "the DVD text data manager"
      c = CandidateProposal.new(
        net: name,
        # High, never certain. A name someone typed is not the same as the name
        # a provider files the work under, and it is occasionally the studio or
        # a box-set label.
        confidence: 0.8,
        why: "read from #{source}"
      )

      work = raw.dup
      notes = []

      # Both patterns anchor at the end, so whichever marker comes last must go
      # first: "Season 3 Disc 2" only yields its season once the disc is gone.
      loop do
        if c.disc_number.nil? && (m = DISC_IN_NAME.match(work))
          work, c.disc_number = m[1], m[2].to_i
          c.kind ||= "series"
          notes << "disc #{m[2]}"
          next
        end
        if c.season.nil? && (m = SEASON_IN_NAME.match(work))
          work, c.season = m[1], m[2].to_i
          c.kind = "series"
          notes << "season #{m[2]}"
          next
        end
        break
      end

      if (m = TRAILING_PAREN.match(work))
        inner = m[2].strip
        if YEAR_ONLY.match?(inner)
          work, c.year = m[1], inner.to_i
          notes << "year #{inner}"
        elsif edition?(inner)
          work = m[1]
          notes << %(edition "#{inner}")
        end
        # Anything else is part of the title. Removing it would break more than
        # it fixed.
      end

      title = tidy(work)
      return [] if title.empty?

      c.title = title
      c.why += " (#{notes.join(', ')})" if notes.any?
      [c]
    end

    private

    def edition?(s)
      l = s.downcase.strip
      EDITIONS.any? { |w| l == w || l.include?(w) }
    end

    # Deliberately light. Unlike a volume label this was typed by a person and
    # mostly wants leaving alone; the one worthwhile change is case, because
    # some discs shout.
    def tidy(s)
      s = s.strip.gsub(/\A[:,\-–—\s]+|[:,\-–—\s]+\z/, "").squeeze(" ").strip
      return "" if s.empty?
      # Mixed case was written the way someone wanted it: eXistenZ survives.
      return s unless s == s.upcase && s != s.downcase
      s.downcase.split(" ").map { |w| w.sub(/\A./, &:upcase) }.join(" ")
    end
  end
end
