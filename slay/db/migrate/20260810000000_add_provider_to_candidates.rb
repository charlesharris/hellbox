# A provider match's id was only ever in its `why` prose -- "TMDB has "Roman
# Holiday" (1953, tmdb:804)" -- which is readable and unusable. The id is the
# single most valuable thing identification produces, because it is what goes
# into the NFO and ends Jellyfin's own guessing, so it becomes a column.
class AddProviderToCandidates < ActiveRecord::Migration[8.1]
  def change
    add_column :candidates, :provider, :string
    add_column :candidates, :provider_id, :string
  end
end
