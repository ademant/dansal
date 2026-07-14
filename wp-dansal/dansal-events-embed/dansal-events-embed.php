<?php
/**
 * Plugin Name: Dansal Events Embed
 * Description: Embed Dansal events calendar or locations map via iframe
 * Version: 2.1.0
 * Author: Balfolk
 */

if (!defined('ABSPATH')) exit;

add_shortcode('dansal_calendar',  'dansal_calendar_shortcode');
add_shortcode('dansal_locations', 'dansal_locations_shortcode');

function dansal_calendar_shortcode($atts = []) {
    $atts = shortcode_atts([
        'embed_url' => get_option('dansal_embed_url', 'https://balfolk.jetzt/embed/calendar'),
        'height'    => '900px',
        'lang'      => 'de',
    ], $atts);

    $src    = esc_url($atts['embed_url'] . '?lang=' . urlencode($atts['lang']));
    $height = esc_attr($atts['height']);

    return '<iframe src="' . $src . '" style="width:100%;height:' . $height . ';border:0" loading="lazy" title="Veranstaltungen"></iframe>';
}

function dansal_locations_shortcode($atts = []) {
    $atts = shortcode_atts([
        'embed_url' => get_option('dansal_embed_url_locations', 'https://balfolk.jetzt/embed/locations'),
        'height'    => '600px',
        'lang'      => 'de',
    ], $atts);

    $src    = esc_url($atts['embed_url'] . '?lang=' . urlencode($atts['lang']));
    $height = esc_attr($atts['height']);

    return '<iframe src="' . $src . '" style="width:100%;height:' . $height . ';border:0" loading="lazy" title="Orte"></iframe>';
}

add_action('admin_menu', function() {
    add_options_page('Dansal Events', 'Dansal Events', 'manage_options', 'dansal-events', 'dansal_settings_page');
});

function dansal_settings_page() {
    if (isset($_POST['submit'])) {
        check_admin_referer('dansal_settings');
        update_option('dansal_embed_url',           sanitize_url($_POST['dansal_embed_url'] ?? ''));
        update_option('dansal_embed_url_locations',  sanitize_url($_POST['dansal_embed_url_locations'] ?? ''));
        echo '<div class="notice notice-success"><p>Settings saved.</p></div>';
    }
    $embed_url           = get_option('dansal_embed_url',           'https://balfolk.jetzt/embed/calendar');
    $embed_url_locations = get_option('dansal_embed_url_locations', 'https://balfolk.jetzt/embed/locations');
    ?>
    <div class="wrap">
        <h1>Dansal Events Settings</h1>
        <form method="post">
            <?php wp_nonce_field('dansal_settings'); ?>
            <table class="form-table">
                <tr>
                    <th><label for="dansal_embed_url">Calendar Embed URL</label></th>
                    <td><input type="url" name="dansal_embed_url" id="dansal_embed_url" value="<?php echo esc_attr($embed_url); ?>" class="regular-text"></td>
                </tr>
                <tr>
                    <th><label for="dansal_embed_url_locations">Locations Embed URL</label></th>
                    <td><input type="url" name="dansal_embed_url_locations" id="dansal_embed_url_locations" value="<?php echo esc_attr($embed_url_locations); ?>" class="regular-text"></td>
                </tr>
            </table>
            <?php submit_button(); ?>
        </form>
    </div>
    <?php
}
