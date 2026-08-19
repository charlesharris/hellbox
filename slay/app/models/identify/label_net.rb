module Identify
  # Reads what it can out of the volume label.
  #
  # The weakest net, written to know it. A label is eleven upper-case characters
  # on the ISO9660 filesystem most DVDs carry, chosen by an authoring house with
  # no interest in being parsed. This collection alone holds STNGD3, STNGD5,
  # STNG4, NEXTGEN and NEXTGEN2 for one series, and six different Still Game
  # discs all called STILL_GAME.
  #
  # Its job is to extract structure, not to be clever. It will not expand STNG
  # into Star Trek: The Next Generation, because a net that guesses confidently
  # is worse than one that abstains — the whole design assumes a stronger net
  # can overrule this one, and it cannot overrule a fabrication that arrived
  # with high confidence.
  class LabelNet
    JUNK = %w[
      dvd_video dvdvideo dvd video_ts bluray blu_ray bd_rom bdrom untitled
      unnamed new_volume disc movie video cdrom default logical_volume_id
      volume my_disc dvd_rom
    ].freeze

    # Suffixes describing the transfer rather than the work. SPACEBALLS_LB is
    # Spaceballs, letterboxed; the LB is not part of the title.
    MARKERS = %w[lb ws fs sf se ce de ee uncut unrated remastered restored
                 ntsc pal r1 r2 dvd bd disc side 16x9 4x3].freeze

    SEASON_DISC = /\A(.*?)[ _-]*s(?:eason)?[ _-]*(\d{1,2})[ _-]*d(?:isc|isk)?[ _-]*(\d{1,2})\z/i
    SEASON      = /\A(.*?)[ _-]*s(?:eason|sn)?[ _-]*(\d{1,2})\z/i
    DISC        = /\A(.*?)[ _-]*d(?:isc|isk)?[ _-]*(\d{1,2})\z/i
    YEAR        = /\A(.*?)[ _-]*((?:19|20)\d{2})\z/
    TRAILING_N  = /\A(.*?[a-z])[ _-]*(\d{1,2})\z/

    def name = "label"

    def identify(disc)
      raw = disc.volume_label.to_s.strip
      return [] if raw.empty?
      return [] if JUNK.include?(raw.downcase.tr(" ", "_"))

      c = CandidateProposal.new(net: name, confidence: 0.5, why: "read from the volume label")
      work = raw

      # Longest pattern first, so STILL_GAME_S3D2 is not read as a disc number
      # with the season left glued to the title.
      if (m = SEASON_DISC.match(work))
        work, c.season, c.disc_number = m[1], m[2].to_i, m[3].to_i
        c.kind, c.confidence, c.why = "series", 0.65, "label gives an explicit season and disc"
      elsif (m = SEASON.match(work))
        work, c.season = m[1], m[2].to_i
        c.kind, c.confidence, c.why = "series", 0.6, "label gives an explicit season"
      elsif (m = DISC.match(work))
        work, c.disc_number = m[1], m[2].to_i
        c.confidence, c.why = 0.55, "label gives an explicit disc number"
      end

      if c.season.nil? && c.disc_number.nil? && (m = YEAR.match(work))
        work, c.year = m[1], m[2].to_i
        c.confidence, c.why = 0.6, "label carries a year"
      end

      work, dropped = strip_markers(work)

      # A bare trailing number, only after markers are gone so DVD2 does not
      # become a title of "DVD". It could be a disc, a season, or part of the
      # name — NEXTGEN2 gives no way to tell — so it is recorded as the
      # commonest and the uncertainty is paid for in confidence.
      if c.season.nil? && c.disc_number.nil? && c.year.nil? && (m = TRAILING_N.match(work.downcase))
        n = m[2].to_i
        if n.positive? && n <= 20
          work = work[0, m[1].length]
          c.disc_number, c.confidence = n, 0.35
          c.why = "label ends in a bare number, which may be a disc or a season"
        end
      end

      title = title_case(work)
      return [] if title.empty?

      c.title = title
      if abbreviated?(title, raw == raw.upcase)
        c.confidence = 0.15
        c.why = "label looks like an abbreviation rather than a title"
      end
      c.why += " (ignored #{dropped.join(', ')})" if dropped.any?
      [c]
    end

    private

    def strip_markers(s)
      parts = s.split(/[_\-.+ ]+/).reject(&:empty?)
      kept, dropped = parts.partition { |p| !MARKERS.include?(p.downcase) }
      # Never strip everything: DISC_2 is better named "Disc" than nothing, and
      # the confidence already says so.
      return [s, []] if kept.empty?
      [kept.join(" "), dropped]
    end

    def title_case(s)
      words = s.split(/[_\-.+ ]+/).reject(&:empty?)
      return "" if words.empty?
      mixed = s != s.upcase
      words.map { |w| mixed ? w : w.downcase.sub(/\A./, &:upcase) }.join(" ")
    end

    # A single short word from an all-caps label with no separator: STNG,
    # NEXTGEN. A genuinely short all-caps title like ALIEN is marked weak by
    # this and that is accepted — it still produces the right name, at a
    # confidence any real evidence beats, and with nothing to contradict it the
    # answer stands. The reverse mistake is the expensive one.
    def abbreviated?(title, was_upper)
      was_upper && !title.include?(" ") && title.length <= 8
    end
  end
end
