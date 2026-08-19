# Runs the nets over a disc and records what they proposed.
#
# Every proposal is stored, not just the winner. A wrong name in the library has
# to be traceable to the evidence that produced it, and agreement between nets
# cannot be recomputed once the losers are discarded.
#
# Re-runnable by design. Identification is the part most likely to be rewritten,
# and being able to replay it over the whole collection — with no disc in the
# drive and no network — is what the catalog exists for.
class IdentifyDisc
  NETS = [Identify::DiscNameNet, Identify::LabelNet, Identify::ShapeNet].freeze

  def self.call(disc) = new(disc).call

  def initialize(disc)
    @disc = disc
  end

  def call
    proposals = NETS.flat_map do |klass|
      klass.new.identify(@disc)
    rescue StandardError => e
      # One net failing must not lose the others' answers. A net that cannot
      # run is not a reason to refuse to name the disc.
      Rails.logger.warn("#{klass}: #{e.class}: #{e.message}")
      []
    end

    # The provider runs last and over what the others found, because it reads a
    # database rather than the disc and needs a name to look up. With no API key
    # configured it returns nothing, which is an ordinary answer.
    begin
      proposals += Identify::ProviderNet.new.identify(@disc, proposals)
    rescue StandardError => e
      Rails.logger.warn("ProviderNet: #{e.class}: #{e.message}")
    end

    result = Identify::Resolver.call(proposals)

    @disc.transaction do
      @disc.candidates.destroy_all
      result.all.each do |p|
        @disc.candidates.create!(
          net: p.net, title: p.title, year: p.year, kind: p.kind,
          season: p.season, disc_number: p.disc_number,
          provider: p.provider, provider_id: p.provider_id,
          confidence: p.confidence, why: p.why
        )
      end

      status = next_status(result)
      adopt(result.best) if status == "confirmed" && @disc.confirmed_title.blank?
      @disc.update!(status:)
    end

    result
  end

  private

  # Writes the winning proposal into the disc's own confirmed fields.
  #
  # Without this, a disc the nets settled on their own was marked confirmed and
  # left with no title anywhere except among its candidates — so filing, which
  # reads the confirmed fields and nothing else, had nothing to file it under.
  # That is the auto-file path in the design, and it was confirmed-but-unnamed.
  #
  # Guarded on confirmed_title being blank, so a re-run can never overwrite
  # what a person decided. The candidates are rewritten on every run; a
  # decision is written once.
  def adopt(best)
    return if best.nil?

    @disc.assign_attributes(
      confirmed_title: best.title,
      confirmed_year: best.year,
      confirmed_kind: best.kind,
      confirmed_season: best.season,
      provider: best.provider,
      provider_id: best.provider_id,
      confirmed_at: Time.current
    )
  end

  # Only ever moves a disc forward out of "rough". A disc someone has already
  # confirmed must not be dragged back into the review queue by a re-run.
  def next_status(result)
    return @disc.status unless @disc.status == "rough"
    return "needs_review" if result.best.nil? || result.contested
    return "needs_review" if result.best.confidence < 0.7
    # A series still needs a person: episode numbers are not in any of this.
    return "needs_review" if result.best.kind == "series"
    "confirmed"
  end
end
