# What a person decided about a disc, and about each title on it.
#
# Episode numbers live directly on disc_titles rather than only as claims
# against provider Episode rows. Both exist for a reason: a claim ties a title
# to a real episode record with an air date and a name, which is what you want
# once a provider has been consulted, but it cannot be made at all until the
# provider has been. Manual numbering has to work with no API key, no network,
# and a series the provider has never heard of.
class AddReviewFields < ActiveRecord::Migration[8.1]
  def change
    change_table :disc_titles, bulk: true do |t|
      t.integer :season_number
      t.integer :episode_number
      t.string  :episode_title

      # Extras are not episodes and must not be numbered as them. Jellyfin
      # files them separately, and a featurette offered as an alternative
      # version of the feature is the specific thing this prevents.
      t.string :role, null: false, default: "unknown" # feature | episode | extra | ignore
    end
    add_index :disc_titles, %i[disc_id season_number episode_number],
              where: "episode_number IS NOT NULL",
              name: "index_disc_titles_on_episode"

    change_table :discs, bulk: true do |t|
      # What the person settled on, as against what the nets proposed. Kept
      # apart from the candidates so a re-run of identification cannot
      # overwrite a decision someone already made.
      t.string  :confirmed_title
      t.integer :confirmed_year
      t.string  :confirmed_kind          # movie | series
      t.integer :confirmed_season
      t.string  :provider
      t.string  :provider_id
      t.datetime :confirmed_at
    end
  end
end
