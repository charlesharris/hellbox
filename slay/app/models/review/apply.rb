module Review
  # Records what a person decided.
  #
  # Kept apart from the candidates a net proposed, so re-running identification
  # can never overwrite a decision someone already made. That separation is the
  # whole reason a re-run is safe to do at any time.
  class Apply
    def self.call(disc, attrs, titles: nil) = new(disc, attrs, titles).call

    def initialize(disc, attrs, titles)
      @disc = disc
      @attrs = attrs
      @titles = titles
    end

    def call
      @disc.transaction do
        @disc.assign_attributes(
          confirmed_title: presence(:confirmed_title) || @disc.confirmed_title,
          confirmed_year: int(:confirmed_year),
          confirmed_kind: presence(:confirmed_kind) || @disc.confirmed_kind,
          confirmed_season: int(:confirmed_season),
          # Kept when the form does not send one. A provider id the nets found
          # is the one field here nobody would think to retype, and losing it
          # on a reopen-and-confirm would quietly cost the NFO its tmdbid.
          provider: presence(:provider) || @disc.provider,
          provider_id: presence(:provider_id) || @disc.provider_id,
          confirmed_at: Time.current,
          status: "confirmed"
        )
        @disc.save!
        apply_titles if @titles.present?
      end
      @disc
    end

    private

    def presence(k) = @attrs[k].presence
    def int(k) = @attrs[k].presence&.to_i

    def apply_titles
      @titles.each do |index, t|
        row = @disc.disc_titles.find_by(index: index.to_i) or next
        row.role = t[:role].presence || row.role
        row.episode_title = t[:episode_title].presence

        # Only an episode carries numbers. Clearing them when the role changes
        # is what stops a title demoted to an extra keeping an episode number
        # it no longer has any business with.
        if row.role == "episode"
          row.season_number  = t[:season_number].presence&.to_i || @disc.confirmed_season
          row.episode_number = t[:episode_number].presence&.to_i
        else
          row.season_number = nil
          row.episode_number = nil
        end
        row.save!
      end
    end
  end
end
