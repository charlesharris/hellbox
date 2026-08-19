# Where the bridge stopped reading.
#
# Without this, a Rails restart mid-rip loses every event that landed while it
# was down, and the catalog quietly disagrees with the filesystem. The daemon
# keeps a replay buffer and honours Last-Event-ID, so a stored cursor is the
# whole recovery mechanism.
class CreateBridgeCursors < ActiveRecord::Migration[8.1]
  def change
    create_table :bridge_cursors do |t|
      t.string :name, null: false
      t.bigint :last_event_id, null: false, default: 0
      t.datetime :seen_at
      t.timestamps
    end
    add_index :bridge_cursors, :name, unique: true
  end
end
