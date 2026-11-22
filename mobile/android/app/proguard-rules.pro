# Keep native WebView JS bridge methods intact if minification is enabled later
-keepclassmembers class com.mrpoundsign.poundcontrol.MainActivity$NativeBridge {
    @android.webkit.JavascriptInterface <methods>;
}
