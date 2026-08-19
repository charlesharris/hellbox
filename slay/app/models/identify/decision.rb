module Identify
  # Whether what the nets came back with is good enough to act on.
  #
  # Kept apart from IdentifyDisc, which persists, because this is the judgement
  # and that is the bookkeeping. The judgement is the part that decides whether
  # a disc is filed into the library with nobody looking at it, so it is the
  # part that has to be checkable — and until it lived here it could not be run
  # without a database.
  module Decision
    # Below this a proposal goes to a person.
    CONFIRM_AT = 0.7

    # Only ever moves a disc forward out of "rough". A disc someone has already
    # confirmed must not be dragged back into the review queue by a re-run.
    def self.status(result, current:)
      return current unless current == "rough"

      best = result.best
      return "needs_review" if best.nil? || result.contested
      return "needs_review" if best.confidence < CONFIRM_AT

      # A series still needs a person: episode numbers are not in any of this.
      return "needs_review" if best.kind == "series"

      # Confidence answers "do we know what this disc is". It does not answer
      # "is this name good enough to write into the library", and the two came
      # apart the moment a rejected provider match stopped donating its id.
      #
      # A disc whose name is read straight off the DVD text data manager scores
      # 0.8 on that alone. Believing it means filing Movies/The Karate Kid/ with
      # no year and an NFO with no tmdbid — at which point Jellyfin re-matches
      # the file by its name, which is the whole thing §5.4 exists to stop, and
      # exactly the library Phase F has to go back and clean up.
      #
      # So it is a missing field rather than a low score, and it is checked as
      # one. Nothing here is discarded: the disc keeps the name the nets found,
      # and review already shows the disc's own name beside the volume label
      # with a place to supply the id. That is one field of work, once.
      #
      # The cost is accepted deliberately: with no TMDB_API_KEY set the provider
      # net abstains, so no film auto-files at all. A free key is a two-minute
      # fix and the alternative is silently rebuilding the library that v2
      # exists to replace. Decided with the user 2026-08-19.
      return "needs_review" if best.provider_id.blank?

      # FileDisc refuses a disc with no kind — "nothing has confirmed what this
      # disc is" — so confirming one only parks it. Parked is invisible: the
      # dashboard shows what needs review, what is working and what was filed,
      # and a confirmed disc that filing declined is in none of those. Send it
      # to the one place a person will see it.
      return "needs_review" if best.kind.blank?

      "confirmed"
    end
  end
end
