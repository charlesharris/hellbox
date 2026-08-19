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
    #
    # design-v2.md §5.2 sets the auto-file bar at 0.85, and this is 0.7. The
    # gap is deliberate for now but it is not agreed: see hb-1hq.12.
    CONFIRM_AT = 0.7

    # Only ever moves a disc forward out of "rough". A disc someone has already
    # confirmed must not be dragged back into the review queue by a re-run.
    def self.status(result, current:)
      return current unless current == "rough"
      return "needs_review" if result.best.nil? || result.contested
      return "needs_review" if result.best.confidence < CONFIRM_AT
      # A series still needs a person: episode numbers are not in any of this.
      return "needs_review" if result.best.kind == "series"
      "confirmed"
    end
  end
end
