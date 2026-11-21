# Keep native WebView JS bridge methods intact if minification is enabled later
-keepclassmembers class com.mrpoundsign.pounddeck.MainActivity$NativeBridge {
    @android.webkit.JavascriptInterface <methods>;
}
